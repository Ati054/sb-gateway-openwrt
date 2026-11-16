package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestStartupRollbackRetainsScopedProtectionAndRestoresDNS(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	for _, name := range []string{"capture-protected.active", "crash-fallback.active", "dns-interop.json"} {
		if err := os.WriteFile(filepath.Join(paths.RunRoot, name), []byte("old state"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	removed, restored := false, false
	err := rollbackStartup(paths, func(ctx context.Context) error {
		if ctx.Err() != nil || !StartupFailed(paths) {
			t.Fatal("rollback lacks a live cleanup context or retry marker")
		}
		removed = true
		return nil
	}, func() error {
		if !removed {
			t.Fatal("DNS restored before removing interception")
		}
		restored = true
		return nil
	})
	if err != nil || !removed || !restored || !StartupFailed(paths) || ProtectionActive(paths) || !CrashFallbackActive(paths) {
		t.Fatalf("incomplete rollback: removed=%v DNS=%v err=%v", removed, restored, err)
	}
	if _, err := os.Stat(dnsInteropPath(paths)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale DNS state can revive capture", err)
	}
}

func TestStartupRollbackAttemptsDNSWhenPolicyReplacementFails(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	removeErr, dnsErr := errors.New("nft unavailable"), errors.New("resolver restore failed")
	dnsAttempted := false
	err := rollbackStartup(paths, func(context.Context) error { return removeErr }, func() error {
		dnsAttempted = true
		return dnsErr
	})
	if !dnsAttempted || !errors.Is(err, removeErr) || !errors.Is(err, dnsErr) || !StartupFailed(paths) {
		t.Fatal("rollback skipped a recovery action or discarded errors", err)
	}
}

func TestLateHotplugCannotReinstallGuardAfterStartupRollback(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	hotplugDone := make(chan error, 1)
	state := filepath.Join(paths.RunRoot, "firewall-state")
	go func() {
		hotplugDone <- withCaptureLifecycle(ctx, paths, func() error {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return os.WriteFile(state, []byte("guard"), 0600)
		})
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("hotplug did not start")
	}
	rollbackDone := make(chan error, 1)
	go func() {
		rollbackDone <- rollbackStartup(paths, func(context.Context) error {
			return os.WriteFile(state, []byte("WAN"), 0600)
		}, func() error { return nil })
	}()
	// Without serialization rollback can finish while the old hotplug still
	// holds its stale decision, which would then overwrite WAN with a guard.
	var rollbackErr error
	finished := false
	select {
	case rollbackErr = <-rollbackDone:
		finished = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-hotplugDone; err != nil {
		t.Fatal(err)
	}
	if !finished {
		rollbackErr = <-rollbackDone
	}
	if rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	body, err := os.ReadFile(state)
	if err != nil || string(body) != "WAN" {
		t.Fatal("late hotplug replaced rollback with a dead guard", string(body), err)
	}
}

func TestCaptureLifecycleWaitCanBeCancelled(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	err := withCaptureLifecycle(context.Background(), paths, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		return withCaptureLifecycle(ctx, paths, func() error {
			t.Fatal("capture lock was acquired by two owners")
			return nil
		})
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("capture lock ignored the cleanup deadline", err)
	}
}

func TestCancelledServiceRollsBackUnlessInitOwnsThisPID(t *testing.T) {
	for _, tc := range []struct {
		name, stopping string
		cause          error
		wantRollback   bool
	}{
		{"watchdog cancellation", "", context.Canceled, true},
		{"stale stop marker", "not-this-process", context.Canceled, true},
		{"planned restart", strconv.Itoa(os.Getpid()), context.Canceled, false},
		{"fatal worker failure", "", errors.New("health controller failed"), true},
		{"no failure", "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := Paths{RunRoot: t.TempDir()}
			if err := os.WriteFile(filepath.Join(paths.RunRoot, "stopping.pid"), []byte(tc.stopping), 0600); err != nil {
				t.Fatal(err)
			}
			removed, restored := false, false
			err := recoverServiceFailure(paths, tc.cause, func() error {
				return rollbackStartup(paths, func(context.Context) error { removed = true; return nil }, func() error { restored = true; return nil })
			})
			if !errors.Is(err, tc.cause) || removed != tc.wantRollback || restored != tc.wantRollback || StartupFailed(paths) != tc.wantRollback {
				t.Fatalf("cancelled startup recovery: removed=%v DNS=%v err=%v", removed, restored, err)
			}
		})
	}
}

func TestFailureRecoveryDoesNotTouchAnotherLiveController(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	lock, err := acquireController(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := os.WriteFile(protectionFile(paths), []byte("current controller"), 0600); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("second start failed")
	if err := RecoverStartupFailure(paths, cause); !errors.Is(err, cause) {
		t.Fatal("lost the second launch failure", err)
	}
	if !ProtectionActive(paths) || StartupFailed(paths) {
		t.Fatal("failed second launch changed the live controller's network")
	}
}
