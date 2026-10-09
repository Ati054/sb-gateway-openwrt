//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSeparateCoreJoinsExitAndDoesNotLeaveOrphan(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fake-core")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	core, err := startCore("process", nil, "ignored", binary, "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if core.pid == os.Getpid() || core.processName == "" || core.exited == nil {
		t.Fatal("core did not have a separate supervised process")
	}
	if err := syscall.Kill(core.pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-core.exited:
		if err == nil {
			t.Fatal("lost fatal child exit")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child exit not delivered")
	}
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(core.pid, 0); err != syscall.ESRCH {
		t.Fatalf("child not reaped: %v", err)
	}
}

func TestCoreExecutionModeMustBeExplicitAndExecutablePresent(t *testing.T) {
	if _, err := startCore("unknown", nil, "", "", "127.0.0.1:1"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, err := startCore("process", nil, "ignored", "/not/a/core", "127.0.0.1:1"); err == nil {
		t.Fatal("missing binary accepted")
	}
}
