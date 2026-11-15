//go:build linux

package sip003

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInstallStatusDoesNotOpenSpecialFile(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "status.json"), 0600); err != nil {
		t.Fatal(err)
	}
	if state := statusAt(root).State; state.Phase != "" {
		t.Fatal("nonregular status accepted")
	}
}

func TestInstallCommandCancelsInheritedOutputProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	command := pluginCommand(ctx, "sh", "-c", `sleep 3 & echo $! > "$1"; wait`, "fixture", pidfile)
	done := make(chan error, 1)
	go func() { _, err := command.CombinedOutput(); done <- err }()
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(pidfile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(body)))
			if pid > 1 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pid <= 1 {
		cancel()
		<-done
		t.Fatal("fixture child did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled command unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("descendant held output pipe after cancellation")
	}
	if body, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); err == nil {
		_, tail, ok := strings.Cut(string(body), ") ")
		if ok && !strings.HasPrefix(tail, "Z ") && !strings.HasPrefix(tail, "X ") {
			t.Fatal("installation child survived command cancellation")
		}
	}
}
