package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUninstallOnlyRemovesOwnedRecoveryAfterCompletedRestore(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		custom, pending, fail bool
	}{
		{"success", false, false, false}, {"custom", true, false, false}, {"pending", false, true, false}, {"restore-failed", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			paths := Paths{DataRoot: root}
			source, destination := filepath.Join(root, "template"), filepath.Join(root, "boot-hook")
			body := []byte("#!/bin/sh\n# owned recovery\n")
			if err := os.WriteFile(source, body, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.custom {
				body = append(body, []byte("# user changes\n")...)
			}
			if err := os.WriteFile(destination, body, 0600); err != nil {
				t.Fatal(err)
			}
			err := finishDNSUninstall(paths, source, destination, func() error {
				if tc.pending {
					return os.WriteFile(dnsmasqHandoffFile(paths), []byte(`{}`), 0600)
				}
				if tc.fail {
					return errors.New("simulated UCI commit failure")
				}
				return nil
			})
			_, statErr := os.Stat(destination)
			removed := errors.Is(statErr, os.ErrNotExist)
			if removed != (!tc.custom && !tc.pending && !tc.fail) || (err != nil) != (tc.pending || tc.fail) {
				t.Fatal("uninstall lost recovery or deleted custom state", removed, err)
			}
		})
	}
}
