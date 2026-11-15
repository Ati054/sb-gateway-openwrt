package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDNSCleanupSlowRouterStillRestoresPreviousOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns-handoff.json")
	if err := writeDNSHandoff(path, dnsHandoffState{WasEnabled: true, WasRunning: true}); err != nil {
		t.Fatal(err)
	}
	enabled, running := false, false
	err := restoreDNSWithinBudget(t.Context(), 200*time.Millisecond, 100*time.Millisecond,
		func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		func(ctx context.Context) error {
			return restoreDNSHandoff(path, dnsHandoffOps{
				installed: func() bool { return true },
				enabled:   func() bool { return enabled }, running: func() bool { return running },
				err: ctx.Err,
				action: func(action string) error {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if action == "enable" {
						enabled = true
					}
					if action == "start" {
						running = true
					}
					return nil
				},
			})
		})
	if !errors.Is(err, context.DeadlineExceeded) || !enabled || !running {
		t.Fatalf("router cleanup exhausted previous-owner recovery: enabled=%v running=%v err=%v", enabled, running, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("verified owner recovery kept marker", err)
	}
}

func TestDNSCleanupUsesOneTotalDeadlineAndPreservesBothFailures(t *testing.T) {
	routerErr := errors.New("router restore failed")
	var ownerDeadline time.Time
	started := time.Now()
	err := restoreDNSWithinBudget(t.Context(), 100*time.Millisecond, 50*time.Millisecond,
		func(context.Context) error { return routerErr },
		func(ctx context.Context) error { ownerDeadline, _ = ctx.Deadline(); <-ctx.Done(); return ctx.Err() })
	if !errors.Is(err, routerErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cleanup dropped a failure", err)
	}
	if ownerDeadline.After(started.Add(150 * time.Millisecond)) {
		t.Fatal("previous owner received a new overall deadline")
	}
}

func TestDNSCleanupReservesOwnerTimeWithShorterParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	ownerLive := false
	_ = restoreDNSWithinBudget(ctx, time.Second, 200*time.Millisecond,
		func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		func(ctx context.Context) error { ownerLive = ctx.Err() == nil; return ctx.Err() })
	if !ownerLive {
		t.Fatal("a shorter caller deadline consumed the previous owner's reserved recovery time")
	}
}

func TestRuntimeCleanupOverlapsIndependentClosuresAndJoinsErrors(t *testing.T) {
	coreStarted, dnsStarted, releaseDNS := make(chan struct{}), make(chan struct{}), make(chan struct{})
	coreErr, dnsErr := errors.New("core close"), errors.New("DNS restore")
	finished := make(chan error, 1)
	defer close(releaseDNS)
	go func() {
		finished <- closeCoreAndRestoreDNS(func() error {
			close(coreStarted)
			<-dnsStarted
			return coreErr
		}, func() error {
			close(dnsStarted)
			<-releaseDNS
			return dnsErr
		})
	}()
	for _, started := range []<-chan struct{}{coreStarted, dnsStarted} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("independent cleanup did not start concurrently")
		}
	}
	select {
	case <-finished:
		t.Fatal("cleanup returned before DNS restoration completed")
	default:
	}
	// Allow restoration to finish after checking that cleanup still waits.
	releaseDNS <- struct{}{}
	select {
	case err := <-finished:
		if !errors.Is(err, coreErr) || !errors.Is(err, dnsErr) {
			t.Fatalf("cleanup lost an error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish after both operations completed")
	}
}

func TestRuntimeCleanupWithoutDNSHandoff(t *testing.T) {
	want := errors.New("core close")
	calls := 0
	err := closeCoreAndRestoreDNS(func() error { calls++; return want }, nil)
	if calls != 1 || !errors.Is(err, want) {
		t.Fatalf("unexpected core cleanup: calls=%d err=%v", calls, err)
	}
}
