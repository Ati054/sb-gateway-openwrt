//go:build linux

package sip003

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func privateInstallRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInstallStatusCannotRemoveAnActiveLock(t *testing.T) {
	root := privateInstallRoot(t)
	if err := os.WriteFile(filepath.Join(root, "status.json"), []byte(`{"phase":"queued","pid":9999999}`), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireInstallLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if state := statusAt(root).State; state.Phase != "error" {
		t.Fatalf("stale status not diagnosed: %+v", state)
	}
	if other, err := acquireInstallLock(root); err == nil {
		other.Close()
		t.Fatal("status read released a live installer's admission lock")
	}
	if _, err := os.Stat(filepath.Join(root, "install.lock")); err != nil {
		t.Fatal("status removed stable lock inode")
	}
}

func TestInstallLockTransfersToWorkerAndRecoversAfterCrash(t *testing.T) {
	root := privateInstallRoot(t)
	lock, err := acquireInstallLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestInstallLockWorkerHelper$")
	child.Env = append(os.Environ(), "SB_INSTALL_LOCK_HELPER="+root)
	child.ExtraFiles = []*os.File{lock}
	child.Stdin = input
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "LOCKED\n" {
		t.Fatalf("worker did not inherit lock: %q, %v", line, err)
	}
	lock.Close()
	if other, err := acquireInstallLock(root); err == nil {
		other.Close()
		t.Fatal("parent exit released worker's lock")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	other, err := acquireInstallLock(root)
	if err != nil {
		t.Fatalf("crashed worker left installation stuck: %v", err)
	}
	other.Close()
}

func TestInstallLockRejectsReplacedOrSymlinkedEntry(t *testing.T) {
	root := privateInstallRoot(t)
	other, err := os.CreateTemp(root, "other-")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := os.Symlink(other.Name(), filepath.Join(root, "install.lock")); err != nil {
		t.Fatal(err)
	}
	if lock, err := acquireInstallLock(root); err == nil {
		lock.Close()
		t.Fatal("symlinked lock accepted")
	}
}

func TestInstallStatusBoundsStateAndRejectsReusedPID(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "status.json"), []byte(strings.Repeat(" ", 8192)+`{"phase":"success"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if state := statusAt(root).State; state.Phase != "" {
		t.Fatalf("oversized status accepted: %+v", state)
	}
	if installerAlive(os.Getpid()) {
		t.Fatal("unrelated process PID treated as installer")
	}
}

func TestInstallLockWorkerHelper(t *testing.T) {
	root := os.Getenv("SB_INSTALL_LOCK_HELPER")
	if root == "" {
		return
	}
	lock := os.NewFile(3, "install.lock")
	if lock == nil {
		t.Fatal("missing worker lock")
	}
	defer lock.Close()
	if err := verifyInstallLock(root, lock); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("LOCKED\n")
	io.Copy(io.Discard, os.Stdin)
}
