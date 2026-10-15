//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartRegistersProcdWhenEarlyProtectionFails(t *testing.T) {
	dir := t.TempDir()
	body, err := os.ReadFile("../../files/resproxy.init")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(body), ". /lib/functions.sh", "")
	script = strings.ReplaceAll(script, "/usr/bin/resproxyd", "mock_daemon")
	harness := `
config_load() { :; }
config_get_bool() { eval "$1=1"; }
pgrep() { return 1; }
mock_daemon() { echo "daemon:$*"; return 1; }
logger() { echo "log:$*"; }
procd_open_instance() { echo "open:$*"; }
procd_set_param() { echo "param:$*"; }
procd_close_instance() { echo close; }
extra_command() { :; }
`
	path := filepath.Join(dir, "start.sh")
	if err := os.WriteFile(path, []byte(harness+script+"\nstart_service\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", path).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, want := range []string{"daemon:capture-protect", "early LAN protection failed", "open:gateway", "param:command mock_daemon run", "param:respawn 3600 15 0", "close"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
}

func TestHotplugRefreshesExistingGuardWithoutDaemon(t *testing.T) {
	for _, tc := range []struct {
		name, marker, action   string
		running, enabled, want bool
	}{
		{"boot guard", "capture-protected.active", "ifup", false, true, true},
		{"crash guard", "crash-fallback.active", "ifupdate", false, true, true},
		{"VPN removed before daemon", "capture-protected.active", "ifdown", false, true, true},
		{"explicit stop", "", "ifup", false, true, false},
		{"disabled stale guard", "capture-protected.active", "ifup", false, false, false},
		{"live daemon", "", "ifupdate", true, true, true},
		{"irrelevant event", "capture-protected.active", "update", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body, err := os.ReadFile("../../files/resproxy-hotplug")
			if err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(string(body), ". /lib/functions.sh", "")
			script = strings.ReplaceAll(script, "/var/run/resproxy", dir)
			script = strings.ReplaceAll(script, "/usr/bin/resproxyd", "mock_daemon")
			if tc.marker != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.marker), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			enabled, running := "0", "1"
			if tc.enabled {
				enabled = "1"
			}
			if tc.running {
				running = "0"
			}
			harness := "ACTION=" + tc.action + "\nconfig_load() { :; }\nconfig_get_bool() { eval \"$1=" + enabled + "\"; }\npgrep() { return " + running + "; }\nmock_daemon() { echo \"daemon:$*\"; }\nlogger() { echo \"log:$*\"; }\n"
			path := filepath.Join(dir, "hotplug.sh")
			if err := os.WriteFile(path, []byte(harness+script), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("sh", path).CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if got := strings.Contains(string(out), "daemon:capture-restore"); got != tc.want {
				t.Fatalf("restore=%v, want %v: %s", got, tc.want, out)
			}
		})
	}
}
