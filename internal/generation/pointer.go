package generation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const indexedPointer = "CURRENT-INDEXED"

// Keep CURRENT as the last legacy-compatible catalogue. An older package can
// still start after an upgrade rollback; current code prefers the new pointer.
func (s Store) currentID() (string, error) {
	for _, name := range []string{indexedPointer, "CURRENT"} {
		body, err := os.ReadFile(filepath.Join(s.Root, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		id := strings.TrimSpace(string(body))
		if !validID(id) {
			return "", errors.New("invalid current generation pointer")
		}
		return id, nil
	}
	return "", os.ErrNotExist
}

func (s Store) publicationPointer(id string) (string, error) {
	info, err := os.Lstat(filepath.Join(s.IndexedRoot(), id))
	if err == nil {
		if legacy, checkErr := os.Lstat(filepath.Join(s.Root, "CURRENT")); checkErr == nil {
			if !legacy.Mode().IsRegular() || legacy.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("invalid legacy rollback pointer")
			}
		} else if !errors.Is(checkErr, os.ErrNotExist) {
			return "", checkErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("invalid indexed generation directory")
		}
		return indexedPointer, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "CURRENT", nil
}
