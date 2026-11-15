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
	// Wait reaps the group leader, not every descendant. SIGKILL delivery to
	// the child can still be pending for a scheduler turn when Wait returns.
	if !pluginFixtureChildExited(pid, time.Second) {
		t.Fatal("installation child survived command cancellation")
	}
}

func pluginFixtureChildExited(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		body, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if os.IsNotExist(err) {
			return true
		}
		if err == nil {
			_, tail, ok := strings.Cut(string(body), ") ")
			if ok && (strings.HasPrefix(tail, "Z ") || strings.HasPrefix(tail, "X ")) {
				return true
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInstallExitObserverRejectsParentOnlyCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	command := pluginCommand(ctx, "sh", "-c", `sleep 3 & echo $! > "$1"; wait`, "fixture", pidfile)
	// Negative control: killing only the shell must still be detected as an
	// escaped descendant, even when the output pipe wait has already ended.
	command.Cancel = func() error { return command.Process.Kill() }
	command.WaitDelay = 30 * time.Millisecond
	done := make(chan error, 1)
	go func() { _, err := command.CombinedOutput(); done <- err }()
	defer func() {
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
	}()
	var pid int
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(pidfile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(body)))
			if pid > 1 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("negative fixture output pipe did not close")
	}
	if pid <= 1 {
		t.Fatal("negative fixture child did not start")
	}
	if pluginFixtureChildExited(pid, 50*time.Millisecond) {
		t.Fatal("exit observer accepted a live descendant")
	}
}
