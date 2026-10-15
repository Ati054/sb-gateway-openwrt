package generation

import (
	"encoding/json"
	"errors"
	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const maxMetadataBytes = 2 << 20

type metadata struct {
	Generation string            `json:"generation"`
	Labels     map[string]string `json:"labels"`
}

func (s Store) applyMetadata(id string, nodes []catalog.Node) {
	if s.MetadataRoot == "" {
		return
	}
	f, err := os.Open(filepath.Join(s.MetadataRoot, "catalog-metadata.json"))
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxMetadataBytes {
		return
	}
	var m metadata
	decoder := json.NewDecoder(io.LimitReader(f, maxMetadataBytes+1))
	if decoder.Decode(&m) != nil || decoder.Decode(&struct{}{}) != io.EOF || m.Generation != id {
		return
	}
	for i := range nodes {
		if label, ok := m.Labels[nodes[i].ID]; ok && len(label) <= 1024 && utf8.ValidString(label) {
			// The link parser uses the complete label when no city was supplied.
			// Keep that inferred display value in RAM together with the label.
			if nodes[i].City == nodes[i].Label {
				nodes[i].City = label
			}
			nodes[i].Label = label
		}
	}
}

// PublishMetadata keeps rapidly changing subscription quota labels in bounded
// RAM. It cannot modify endpoints or identities, and is committed only after
// the live catalog transaction succeeds.
func (s Store) PublishMetadata(id string, nodes []catalog.Node) error {
	if s.MetadataRoot == "" {
		return errors.New("RAM metadata directory is required")
	}
	if _, err := s.Load(id); err != nil {
		return err
	}
	labels := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if len(n.Label) > 1024 || !utf8.ValidString(n.Label) {
			return errors.New("invalid metadata label")
		}
		labels[n.ID] = n.Label
	}
	body, err := json.Marshal(metadata{Generation: id, Labels: labels})
	if err != nil {
		return err
	}
	if len(body) > maxMetadataBytes {
		return errors.New("catalog metadata exceeds RAM budget")
	}
	if err := os.MkdirAll(s.MetadataRoot, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.MetadataRoot, ".catalog-metadata-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(body)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(s.MetadataRoot, "catalog-metadata.json"))
}
