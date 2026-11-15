//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ati054/xroute-openwrt/internal/daemon"
)

func TestCaptureRestoreCommandAcceptsExplicitInputOnStoppedService(t *testing.T) {
	paths := daemon.DefaultPaths()
	if running(paths) || daemon.ProtectionActive(paths) || daemon.CrashFallbackActive(paths) {
		t.Skip("requires an isolated host without a router capture lifecycle")
	}
	// Exercise the public dispatcher, including validation before its switch.
	// A stopped lifecycle must accept the internal flag and safely skip work.
	if err := execute([]string{"capture-restore-rules", "--config-stdin"}); err != nil {
		t.Fatal(err)
	}
	if err := execute([]string{"capture-restore-rules", "--unexpected"}); err == nil {
		t.Fatal("unknown renderer flag accepted")
	}
	if binary := os.Getenv("RESPROXY_TEST_XRAY"); binary != "" {
		binary, err := filepath.EvalSymlinks(binary)
		if err != nil {
			t.Fatal(err)
		}
		output, err := exec.Command(binary, "capture-restore-rules", "--config-stdin").CombinedOutput()
		if err != nil || !strings.Contains(string(output), "# resproxy-restore skip") {
			t.Fatalf("packaged command rejected renderer input: %v %s", err, output)
		}
	}
}

func TestCaptureRestoreForwardsExplicitConfigurationUnderFirewallLock(t *testing.T) {
	h := newFirewallHarness(t)
	stub := `#!/bin/sh
[ "$1" = capture-restore-rules ] || exit 1
printf '%s' "${2:-}" > "$TEST_STATE/renderer-argument"
cat > "$TEST_STATE/renderer-input"
echo '# resproxy-restore apply br-lan 12345 1053'
cat "$TEST_STATE/renderer-input"
`
	if err := os.WriteFile(filepath.Join(h.root, "bin/resproxyd"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	payload := `{"enabled":true,"interface":"br-lan","policy":{"DirectIPs":["203.0.113.1/32"]}}`
	cmd := h.command("restore")
	cmd.Args = append(cmd.Args, "--restore-config-stdin")
	cmd.Stdin = strings.NewReader(payload)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore failed: %v %s", err, output)
	}
	argument, _ := os.ReadFile(filepath.Join(h.state, "renderer-argument"))
	input, _ := os.ReadFile(filepath.Join(h.state, "renderer-input"))
	if string(argument) != "--config-stdin" || string(input) != payload {
		t.Fatalf("acknowledged capture snapshot not forwarded: argument=%q input=%q", argument, input)
	}
}
