package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBridgeNetfilterOwnsOnlyChangedFlagsAndRestoresAfterRestart(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	root := t.TempDir()
	for i, name := range bridgeCaptureFlags {
		if err := os.WriteFile(filepath.Join(root, name), []byte([]string{"1\n", "0\n"}[i]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := prepareBridgeNetfilter(paths, root, true); err != nil {
			t.Fatal(err)
		}
	}
	state, err := readBridgeState(paths)
	if err != nil || len(state) != 1 || state[bridgeCaptureFlags[0]] != 1 {
		t.Fatal("lost original values across restart", state, err)
	}
	for _, name := range bridgeCaptureFlags {
		if value, _ := bridgeFlag(root, name); value != "0" {
			t.Fatal("TProxy still sees bridge netfilter", name, value)
		}
	}
	if err := restoreBridgeNetfilter(paths, root); err != nil {
		t.Fatal(err)
	}
	for i, name := range bridgeCaptureFlags {
		if value, _ := bridgeFlag(root, name); value != []string{"1", "0"}[i] {
			t.Fatal("changed an unowned original value", name, value)
		}
	}
	if _, err := os.Stat(bridgeStatePath(paths)); !os.IsNotExist(err) {
		t.Fatal("ownership survived successful restore", err)
	}
}

func TestBridgeNetfilterRecordsBeforePartialWriteAndRetriesRestore(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	root := t.TempDir()
	for _, name := range bridgeCaptureFlags {
		os.WriteFile(filepath.Join(root, name), []byte("1\n"), 0600)
	}
	if err := prepareBridgeNetfilter(paths, root, true); err != nil {
		t.Fatal(err)
	}
	// A temporarily unreadable second flag must not prevent restoring the first.
	os.WriteFile(filepath.Join(root, bridgeCaptureFlags[1]), []byte("invalid"), 0600)
	if err := restoreBridgeNetfilter(paths, root); err == nil {
		t.Fatal("restore failure was hidden")
	}
	if value, _ := bridgeFlag(root, bridgeCaptureFlags[0]); value != "1" {
		t.Fatal("other flag was not restored")
	}
	state, _ := readBridgeState(paths)
	if len(state) != 1 || state[bridgeCaptureFlags[1]] != 1 {
		t.Fatal("lost retry ownership", state)
	}
	os.WriteFile(filepath.Join(root, bridgeCaptureFlags[1]), []byte("0\n"), 0600)
	if err := restoreBridgeNetfilter(paths, root); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeNetfilterMissingModuleIsNoop(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	if err := prepareBridgeNetfilter(paths, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bridgeStatePath(paths)); !os.IsNotExist(err) {
		t.Fatal("claimed ownership without changing sysctl", err)
	}
}

func TestBridgeNetfilterReleasesIPv6WhenSwitchingAwayFromProxy(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	root := t.TempDir()
	for _, name := range bridgeCaptureFlags {
		if err := os.WriteFile(filepath.Join(root, name), []byte("1\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareBridgeNetfilter(paths, root, false); err != nil {
		t.Fatal(err)
	}
	if value, _ := bridgeFlag(root, bridgeCaptureFlags[1]); value != "1" {
		t.Fatal("IPv4-only capture changed the IPv6 bridge flag")
	}
	if err := prepareBridgeNetfilter(paths, root, true); err != nil {
		t.Fatal(err)
	}
	if value, _ := bridgeFlag(root, bridgeCaptureFlags[1]); value != "0" {
		t.Fatal("IPv6 proxy capture was not prepared")
	}
	if err := prepareBridgeNetfilter(paths, root, false); err != nil {
		t.Fatal(err)
	}
	if value, _ := bridgeFlag(root, bridgeCaptureFlags[1]); value != "1" {
		t.Fatal("IPv6 bridge flag was not restored on mode change")
	}
	state, err := readBridgeState(paths)
	if err != nil || len(state) != 1 || state[bridgeCaptureFlags[0]] != 1 {
		t.Fatal("lost IPv4 ownership or retained unused IPv6 ownership", state, err)
	}
}
