// Package generation persists complete subscription snapshots independently
// of the running Xray process. Staging never changes the published snapshot.
package generation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

const maxGenerationBytes = 16 << 20

type Store struct{ Root string }

// Stage writes a complete, content-addressed generation. The public catalog
// and private credentials are separate files; neither becomes current until
// Publish is called after the runtime has confirmed activation.
func (s Store) Stage(nodes []catalog.Node) (string, error) {
	if err := validateNodes(nodes); err != nil {
		return "", err
	}
	public, err := json.Marshal(nodes)
	if err != nil {
		return "", err
	}
	secrets := make(map[string]map[string]string, len(nodes))
	for _, node := range nodes {
		secrets[node.ID] = node.Credentials
	}
	private, err := json.Marshal(secrets)
	if err != nil {
		return "", err
	}
	if len(public)+len(private) > maxGenerationBytes {
		return "", errors.New("generation exceeds size limit")
	}
	id := generationID(public, private)
	base := filepath.Join(s.Root, "generations")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	final := filepath.Join(base, id)
	if _, err := os.Stat(final); err == nil {
		_, loadErr := s.Load(id)
		return id, loadErr
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	staging, err := os.MkdirTemp(base, ".staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if err := writeSynced(filepath.Join(staging, "catalog.json"), public); err != nil {
		return "", err
	}
	if err := writeSynced(filepath.Join(staging, "secrets.json"), private); err != nil {
		return "", err
	}
	if err := syncDirectory(staging); err != nil {
		return "", err
	}
	if err := os.Rename(staging, final); err != nil {
		return "", err
	}
	if err := syncDirectory(base); err != nil {
		return "", err
	}
	return id, nil
}

// Publish atomically changes the small pointer only after a complete staged
// generation has been verified. It does not itself switch Xray.
func (s Store) Publish(id string) error {
	if _, err := s.Load(id); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.Root, ".current-")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.WriteString(id + "\n"); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(path, filepath.Join(s.Root, "CURRENT")); err != nil {
		return err
	}
	return syncDirectory(s.Root)
}

func (s Store) Current() (string, []catalog.Node, error) {
	body, err := os.ReadFile(filepath.Join(s.Root, "CURRENT"))
	if err != nil {
		return "", nil, err
	}
	id := strings.TrimSpace(string(body))
	nodes, err := s.Load(id)
	return id, nodes, err
}

// ClearCurrent removes only the published pointer. Complete generations stay
// on disk for bounded rollback pruning, but no node remains active.
func (s Store) ClearCurrent() error {
	path := filepath.Join(s.Root, "CURRENT")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(s.Root)
}

func (s Store) Load(id string) ([]catalog.Node, error) {
	if !validID(id) {
		return nil, errors.New("invalid generation ID")
	}
	path := filepath.Join(s.Root, "generations", id)
	public, err := readLimited(filepath.Join(path, "catalog.json"))
	if err != nil {
		return nil, err
	}
	private, err := readLimited(filepath.Join(path, "secrets.json"))
	if err != nil {
		return nil, err
	}
	if generationID(public, private) != id {
		return nil, errors.New("generation integrity check failed")
	}
	var nodes []catalog.Node
	var secrets map[string]map[string]string
	if err := json.Unmarshal(public, &nodes); err != nil {
		return nil, errors.New("invalid public catalog")
	}
	if err := json.Unmarshal(private, &secrets); err != nil {
		return nil, errors.New("invalid secret catalog")
	}
	if err := validateNodes(nodes); err != nil {
		return nil, err
	}
	if len(secrets) != len(nodes) {
		return nil, errors.New("generation secret count mismatch")
	}
	for index := range nodes {
		credentials, exists := secrets[nodes[index].ID]
		if !exists {
			return nil, errors.New("generation credentials missing")
		}
		nodes[index].Credentials = credentials
	}
	return nodes, nil
}

// Prune bounds persistent subscription storage. It always keeps CURRENT and
// up to keep-1 newest other generations for rollback. It touches only direct
// child directories whose names are valid content hashes; an interrupted
// staging directory is eligible only after a full day.
func (s Store) Prune(keep int) error {
	if keep < 2 {
		return errors.New("at least two generations must be retained")
	}
	base := filepath.Join(s.Root, "generations")
	baseInfo, err := os.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !baseInfo.IsDir() || baseInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("generation root is not a real directory")
	}
	current := ""
	if body, err := os.ReadFile(filepath.Join(s.Root, "CURRENT")); err == nil {
		current = strings.TrimSpace(string(body))
		if !validID(current) {
			return errors.New("current generation pointer is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	type generationEntry struct {
		name string
		when time.Time
	}
	generations := make([]generationEntry, 0, len(entries))
	staleStaging := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if validID(entry.Name()) {
			generations = append(generations, generationEntry{entry.Name(), info.ModTime()})
		} else if strings.HasPrefix(entry.Name(), ".staging-") && time.Since(info.ModTime()) > 24*time.Hour {
			staleStaging = append(staleStaging, entry.Name())
		}
	}
	if current != "" {
		found := false
		for _, entry := range generations {
			found = found || entry.name == current
		}
		if !found {
			return errors.New("current generation directory is missing")
		}
	}
	sort.Slice(generations, func(i, j int) bool {
		if generations[i].when.Equal(generations[j].when) {
			return generations[i].name > generations[j].name
		}
		return generations[i].when.After(generations[j].when)
	})
	retained := map[string]bool{}
	if current != "" {
		retained[current] = true
	}
	for _, entry := range generations {
		if len(retained) >= keep {
			break
		}
		retained[entry.name] = true
	}
	for _, entry := range generations {
		if !retained[entry.name] {
			if err := removeGenerationChild(base, entry.name); err != nil {
				return err
			}
		}
	}
	for _, name := range staleStaging {
		if err := removeGenerationChild(base, name); err != nil {
			return err
		}
	}
	return syncDirectory(base)
}

func removeGenerationChild(base, name string) error {
	if !validID(name) && !strings.HasPrefix(name, ".staging-") {
		return errors.New("unsafe generation child name")
	}
	target := filepath.Join(base, name)
	if filepath.Dir(target) != base {
		return errors.New("generation child escaped its root")
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("generation child is not a real directory")
	}
	return os.RemoveAll(target)
}

func validateNodes(nodes []catalog.Node) error {
	if len(nodes) == 0 {
		return errors.New("empty generation")
	}
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if node.ID == "" || seen[node.ID] || node.ProviderID == "" || node.Server == "" || node.Port < 1 || node.Port > 65535 {
			return errors.New("invalid or duplicate generation node")
		}
		seen[node.ID] = true
	}
	return nil
}

func validID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func generationID(public, private []byte) string {
	bytes := make([]byte, 0, len(public)+len(private)+1)
	bytes = append(bytes, public...)
	bytes = append(bytes, 0)
	bytes = append(bytes, private...)
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:])
}

func readLimited(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxGenerationBytes {
		return nil, errors.New("generation file exceeds size limit")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(body) > maxGenerationBytes {
		return nil, fmt.Errorf("generation file exceeds size limit")
	}
	return body, nil
}

func writeSynced(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
