//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
