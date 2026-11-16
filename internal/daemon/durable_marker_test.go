package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncedMarkerReportsPublicationBeforeDirectoryFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("directory sync rejected")
	renamed, err := writeSyncedMarker(path, []byte("new"), func(string) error {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "new" {
			t.Fatal("directory sync preceded publication", string(body), err)
		}
		return failure
	})
	if !renamed || !errors.Is(err, failure) {
		t.Fatal("lost post-publication phase", renamed, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("marker privacy changed", err)
	}
}

func TestSyncedMarkerPreservesPreviousStateWhenRenameFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(path, "old")
	if err := os.WriteFile(previous, []byte("acknowledged"), 0600); err != nil {
		t.Fatal(err)
	}
	renamed, err := writeSyncedMarker(path, []byte("new"), func(string) error { t.Fatal("sync attempted before publication"); return nil })
	body, readErr := os.ReadFile(previous)
	if err == nil || renamed || readErr != nil || string(body) != "acknowledged" {
		t.Fatal("pre-publication failure changed previous state", renamed, err, readErr)
	}
}

func TestDurableDNSMarkerDoesNotIgnoreWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state")
	if err := writeDurableDNSMarker(path, []byte("new")); err == nil {
		t.Fatal("DNS ownership marker swallowed a storage error")
	}
}
