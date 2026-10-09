//go:build linux

package daemon

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

func TestBoundedCommandCancelsDescendantsHoldingOutput(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := boundedCommand(ctx, "sh", "-c", `sleep 60 & echo $! > "$1"; mkdir "$1.ready"; wait`, "test", marker)
	done := make(chan error, 1)
	go func() { _, err := command.CombinedOutput(); done <- err }()
	// Wait for a real descendant before cancellation. A fixed 300 ms startup
	// deadline only tested scheduler load on a busy race-instrumented host.
	readyDeadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker + ".ready"); err == nil {
			break
		}
		if time.Now().After(readyDeadline) {
			cancel()
			<-done
			t.Fatal("descendant did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled shell succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("descendant output held shutdown open")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("shutdown deadline exceeded")
	}
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	// SIGKILL delivery to a descendant is asynchronous to reaping its parent.
	deadline := time.Now().Add(time.Second)
	for {
		body, err = os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if os.IsNotExist(err) || (err == nil && strings.Contains(string(body), ") Z ")) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("descendant survived cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
