package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/subscriptioncatalog"
)

// RecordRoot describes an immutable private catalogue, not an active reader.
// Nodes in a metadata snapshot share it; definitions are resolved on demand.
type RecordRoot struct {
	Path string                  `json:"path"`
	Ref  subscriptioncatalog.Ref `json:"ref"`
}

type RecordLocator struct {
	Root *RecordRoot
	ID   string
}

type IdentitySet struct {
	Label       string `json:"label"`
	Stable      string `json:"stable"`
	Endpoint    string `json:"endpoint"`
	Runtime     string `json:"runtime"`
	Credentials string `json:"credentials"`
}

type RecordMetadata struct {
	Node             Node        `json:"node"`
	Identities       IdentitySet `json:"identities"`
	DefinitionDigest string      `json:"definition_digest,omitempty"`
	RuntimeTagDigest string      `json:"runtime_tag_digest,omitempty"`
}

type RecordSecrets struct {
	Credentials map[string]string `json:"credentials"`
	Definition  json.RawMessage   `json:"definition,omitempty"`
}

func Describe(node Node) IdentitySet {
	return IdentitySet{Label: node.Label, Stable: stableIdentity(node), Endpoint: endpointIdentity(node), Runtime: runtimeIdentity(node), Credentials: credentialIdentity(node)}
}

func Metadata(node Node, locator *RecordLocator, digest string) Node {
	keys := Describe(node)
	node.Settings, node.Credentials = nil, nil
	node.Record, node.Identities, node.DefinitionDigest = locator, &keys, digest
	return node
}

// Materialize returns one independent connection. It never expands the rest of
// the catalogue or exposes credentials to public metadata and status snapshots.
func (node Node) Materialize(ctx context.Context) (Node, error) {
	if node.Record == nil {
		return node, nil
	}
	record, err := LookupRecord(ctx, node.Record)
	if err != nil {
		return Node{}, err
	}
	var full Node
	var secret RecordSecrets
	if json.Unmarshal(record.Node, &full) != nil || json.Unmarshal(record.Secrets, &secret) != nil {
		return Node{}, errors.New("invalid private node record")
	}
	full.Credentials = secret.Credentials
	if full.ProviderID != node.ProviderID || node.Identities == nil || runtimeIdentity(full) != node.Identities.Runtime {
		return Node{}, errors.New("node record does not match its metadata")
	}
	full.ID, full.SelectionIdentity = node.ID, node.SelectionIdentity
	full.Label, full.Country, full.City = node.Label, node.Country, node.City
	return full, nil
}

type recordReaderEntry struct {
	root   RecordRoot
	reader *subscriptioncatalog.Reader
	used   time.Time
	pins   int
}

var recordReaders struct {
	sync.Mutex
	entries []*recordReaderEntry
}

// Only a few verified readers are kept open. A lookup holds the cache lock
// through ReadAt, so eviction cannot close a reader while another lane uses it.
// The underlying reader retains file handles, not full record arrays.
func readerEntryLocked(root RecordRoot) (*recordReaderEntry, error) {
	for _, entry := range recordReaders.entries {
		if entry.root == root {
			entry.used = time.Now()
			return entry, nil
		}
	}
	reader, err := subscriptioncatalog.Open(root.Path, root.Ref)
	if err != nil {
		return nil, err
	}
	if len(recordReaders.entries) >= 3 {
		oldest := -1
		for i, entry := range recordReaders.entries {
			if entry.pins == 0 && (oldest < 0 || entry.used.Before(recordReaders.entries[oldest].used)) {
				oldest = i
			}
		}
		if oldest >= 0 {
			_ = recordReaders.entries[oldest].reader.Close()
			recordReaders.entries = append(recordReaders.entries[:oldest], recordReaders.entries[oldest+1:]...)
		}
	}
	entry := &recordReaderEntry{root: root, reader: reader, used: time.Now()}
	recordReaders.entries = append(recordReaders.entries, entry)
	return entry, nil
}

func LookupRecord(ctx context.Context, locator *RecordLocator) (subscriptioncatalog.Record, error) {
	if locator == nil || locator.Root == nil || locator.ID == "" {
		return subscriptioncatalog.Record{}, errors.New("missing node record reference")
	}
	if err := ctx.Err(); err != nil {
		return subscriptioncatalog.Record{}, err
	}
	recordReaders.Lock()
	defer recordReaders.Unlock()
	entry, err := readerEntryLocked(*locator.Root)
	if err != nil {
		return subscriptioncatalog.Record{}, err
	}
	return entry.reader.Get(ctx, locator.ID)
}

// PinRecordRoot retains a shared generation lease for one live runtime view.
// Pruning cannot remove old definitions until every lane has moved on.
func PinRecordRoot(ctx context.Context, root *RecordRoot) (func(), error) {
	if root == nil {
		return func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	recordReaders.Lock()
	entry, err := readerEntryLocked(*root)
	if err != nil {
		recordReaders.Unlock()
		return nil, err
	}
	entry.pins++
	recordReaders.Unlock()
	var once sync.Once
	return func() { once.Do(func() { recordReaders.Lock(); entry.pins--; recordReaders.Unlock() }) }, nil
}

// ReleaseRecordReaders is called before pruning or removing this caller-owned
// catalogue root. Lookup and close are serialized; no in-flight reader is lost.
func ReleaseRecordReaders(path string) {
	recordReaders.Lock()
	defer recordReaders.Unlock()
	kept := recordReaders.entries[:0]
	for _, entry := range recordReaders.entries {
		if entry.root.Path == path && entry.pins == 0 {
			_ = entry.reader.Close()
		} else {
			kept = append(kept, entry)
		}
	}
	clear(recordReaders.entries[len(kept):])
	recordReaders.entries = kept
}

func StoredDefinition(ctx context.Context, locator *RecordLocator) (json.RawMessage, error) {
	record, err := LookupRecord(ctx, locator)
	if err != nil {
		return nil, err
	}
	var secret RecordSecrets
	if json.Unmarshal(record.Secrets, &secret) != nil || !json.Valid(secret.Definition) {
		return nil, errors.New("stored outbound definition unavailable")
	}
	return secret.Definition, nil
}
