package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/subscriptioncatalog"
)

type DefinitionEncoder func(catalog.Node) (json.RawMessage, error)

// NodeBuilder writes private connection records one at a time. The live view
// contains metadata and immutable references, never every outbound definition.
type NodeBuilder struct {
	builder *subscriptioncatalog.Builder
	root    string
	encode  DefinitionEncoder
}

func NewNodeBuilder(root string, encode DefinitionEncoder) (*NodeBuilder, error) {
	b, err := subscriptioncatalog.NewBuilder(root, subscriptioncatalog.Limits{MaxCanonicalBytes: maxGenerationBytes, MaxRecordBytes: 1 << 20, MaxDiskBytes: 128 << 20, MinFreeBytes: 2 << 20})
	if err != nil {
		return nil, err
	}
	return &NodeBuilder{builder: b, root: root, encode: encode}, nil
}

func (b *NodeBuilder) Append(ctx context.Context, id string, node catalog.Node) error {
	full, err := node.Materialize(ctx)
	if err != nil {
		return err
	}
	var definition json.RawMessage
	if b.encode != nil {
		definition, err = b.encode(full)
	} else if node.Record != nil {
		definition, _ = catalog.StoredDefinition(ctx, node.Record)
	}
	if err != nil {
		return err
	}
	if len(definition) > 0 && !json.Valid(definition) {
		return errors.New("invalid outbound definition")
	}
	digest, tagDigest := "", ""
	if len(definition) > 0 {
		sum := sha256.Sum256(definition)
		digest = hex.EncodeToString(sum[:])
		tagSum := sha256.Sum256(append([]byte(full.ID+"\x00"), definition...))
		tagDigest = hex.EncodeToString(tagSum[:])[:12]
	}
	keys := catalog.Describe(full)
	metadata := catalog.Metadata(full, nil, digest)
	data, err := json.Marshal(catalog.RecordMetadata{Node: metadata, Identities: keys, DefinitionDigest: digest, RuntimeTagDigest: tagDigest})
	if err != nil {
		return err
	}
	body, err := json.Marshal(full)
	if err != nil {
		return err
	}
	secret, err := json.Marshal(catalog.RecordSecrets{Credentials: full.Credentials, Definition: definition})
	if err != nil {
		return err
	}
	return b.builder.Append(ctx, subscriptioncatalog.Record{ID: id, Metadata: data, Node: body, Secrets: secret})
}

func (b *NodeBuilder) Abort() error { return b.builder.Abort() }
func (b *NodeBuilder) Seal(ctx context.Context) (*catalog.RecordRoot, error) {
	ref, err := b.builder.Seal(ctx)
	if err != nil {
		return nil, err
	}
	return &catalog.RecordRoot{Path: b.root, Ref: ref}, nil
}

func ReadNodeMetadata(ctx context.Context, root *catalog.RecordRoot) ([]catalog.Node, error) {
	reader, err := subscriptioncatalog.Open(root.Path, root.Ref)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var nodes []catalog.Node
	err = reader.WalkMetadata(ctx, func(record subscriptioncatalog.MetadataRecord) error {
		var item catalog.RecordMetadata
		if json.Unmarshal(record.Data, &item) != nil || item.Node.Settings != nil || item.Node.Credentials != nil || item.Identities.Runtime == "" {
			return errors.New("invalid node metadata")
		}
		item.Node.Record = &catalog.RecordLocator{Root: root, ID: record.ID}
		item.Node.Identities = &item.Identities
		item.Node.DefinitionDigest = item.DefinitionDigest
		item.Node.RuntimeTagDigest = item.RuntimeTagDigest
		nodes = append(nodes, item.Node)
		return nil
	})
	return nodes, err
}

func (s Store) IndexedRoot() string { return filepath.Join(s.Root, "indexed") }

func (s Store) StageIndexed(ctx context.Context, nodes []catalog.Node, encode DefinitionEncoder) (string, error) {
	if err := validateNodes(nodes); err != nil {
		return "", err
	}
	b, err := NewNodeBuilder(s.IndexedRoot(), encode)
	if err != nil {
		return "", err
	}
	defer b.Abort()
	for _, node := range nodes {
		if err := b.Append(ctx, node.ID, node); err != nil {
			return "", err
		}
	}
	root, err := b.Seal(ctx)
	if err != nil {
		return "", err
	}
	return root.Ref.Generation, nil
}

// LoadMetadata supports both previously published legacy snapshots and the new
// indexed format. Legacy data is retained until a successful new publication.
func (s Store) LoadMetadata(id string) ([]catalog.Node, error) {
	if !validID(id) {
		return nil, errors.New("invalid generation ID")
	}
	_, err := os.Lstat(filepath.Join(s.IndexedRoot(), id))
	if errors.Is(err, os.ErrNotExist) {
		return s.Load(id)
	}
	if err != nil {
		return nil, err
	}
	ref, err := subscriptioncatalog.Inspect(s.IndexedRoot(), id)
	if err != nil {
		return nil, err
	}
	nodes, err := ReadNodeMetadata(context.Background(), &catalog.RecordRoot{Path: s.IndexedRoot(), Ref: ref})
	if err != nil {
		return nil, err
	}
	if err := validateNodes(nodes); err != nil {
		return nil, err
	}
	for _, node := range nodes {
		if node.Record.ID != node.ID {
			return nil, errors.New("indexed node ID does not match its metadata")
		}
	}
	return nodes, nil
}

func (s Store) CurrentMetadata() (string, []catalog.Node, error) {
	id, err := s.currentID()
	if err != nil {
		return "", nil, err
	}
	nodes, err := s.LoadMetadata(id)
	if err == nil {
		s.applyMetadata(id, nodes)
	}
	return id, nodes, err
}

func (s Store) pruneIndexed(keep int) error {
	entries, err := os.ReadDir(s.IndexedRoot())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	current, _ := s.currentID()
	type version struct {
		id    string
		stamp int64
	}
	var versions []version
	for _, entry := range entries {
		if !validID(entry.Name()) || !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		versions = append(versions, version{entry.Name(), info.ModTime().UnixNano()})
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].stamp > versions[j].stamp })
	catalog.ReleaseRecordReaders(s.IndexedRoot())
	remaining := keep - 1
	for _, item := range versions {
		if item.id == current {
			continue
		}
		if remaining > 0 {
			remaining--
			continue
		}
		ref, err := subscriptioncatalog.Inspect(s.IndexedRoot(), item.id)
		if err != nil {
			return err
		}
		if err := subscriptioncatalog.RemoveGeneration(context.Background(), s.IndexedRoot(), ref); err != nil && !errors.Is(err, subscriptioncatalog.ErrBusy) {
			return err
		}
	}
	return nil
}
