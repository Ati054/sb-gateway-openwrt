//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDNSHotfixMigrationPreservesLocalChangesAndMode(t *testing.T) {
	for _, custom := range []string{"", "# user customization\n"} {
		t.Run(map[bool]string{false: "stock", true: "custom"}[custom != ""], func(t *testing.T) {
			root := t.TempDir()
			init := filepath.Join(root, "resproxy")
			base := "#!/bin/sh\necho service\n"
			line := "\tprocd_append_param env PATH=/usr/libexec/resproxy-nft-hotfix:/usr/sbin:/usr/bin:/sbin:/bin # RESPROXY_NFT_HOTFIX\n"
			for name, body := range map[string]string{init: base + line + custom, init + ".apk-new": base} {
				if err := os.WriteFile(name, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			source, err := os.ReadFile("../../files/resproxy-migrate-dns-hotfix")
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(root, "migrate.sh")
			if err := os.WriteFile(script, []byte(strings.ReplaceAll(string(source), "/etc/init.d/resproxy", init)), 0700); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if output, err := exec.Command("sh", script).CombinedOutput(); err != nil {
					t.Fatalf("migration: %v %s", err, output)
				}
				body, err := os.ReadFile(init)
				if err != nil || string(body) != base+custom {
					t.Fatalf("local changes lost: %q, %v", body, err)
				}
				info, err := os.Stat(init)
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatalf("init mode changed: %v, %v", info, err)
				}
			}
			_, err = os.Stat(init + ".apk-new")
			if (err == nil) != (custom != "") {
				t.Fatalf("wrong protected candidate handling: %v", err)
			}
		})
	}
}

func TestDNSHotfixMigrationRejectsUnknownMarker(t *testing.T) {
	root := t.TempDir()
	init := filepath.Join(root, "resproxy")
	body := "#!/bin/sh\n# RESPROXY_NFT_HOTFIX custom unrelated change\n"
	if err := os.WriteFile(init, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../files/resproxy-migrate-dns-hotfix")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "migrate.sh")
	if err := os.WriteFile(script, []byte(strings.ReplaceAll(string(source), "/etc/init.d/resproxy", init)), 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("sh", script).CombinedOutput(); err == nil {
		t.Fatalf("unrecognized marker was modified: %s", output)
	}
	if after, err := os.ReadFile(init); err != nil || string(after) != body {
		t.Fatalf("unrecognized init changed: %s, %v", after, err)
	}
}
