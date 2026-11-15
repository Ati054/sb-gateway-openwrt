package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDNSCoexistenceRequiresRestoredPreviousOwner(t *testing.T) {
	failure := errors.New("previous resolver could not restart")
	for _, restoreErr := range []error{nil, failure} {
		calls := 0
		state, err := settleDNSInterop(t.Context(), "proxy-only", true,
			func(context.Context) (bool, error) { t.Fatal("running owner already detected"); return false, nil },
			func(context.Context) (string, error) { return "smartdns", nil },
			func() error { calls++; return restoreErr })
		if calls != 1 || !errors.Is(err, restoreErr) {
			t.Fatalf("restoration failure was ignored: calls=%d err=%v want=%v", calls, err, restoreErr)
		}
		if err == nil && !dnsInteropReady(state) {
			t.Fatalf("verified coexistence was rejected: %+v", state)
		}
	}
}

func TestDNSInteropDoesNotRestoreAnUnownedResolver(t *testing.T) {
	for _, handedOff := range []bool{false, true} {
		state, err := settleDNSInterop(t.Context(), "proxy-only", handedOff,
			func(context.Context) (bool, error) { return false, nil },
			func(context.Context) (string, error) { return "", nil },
			func() error { t.Fatal("working managed DNS restored an external owner"); return nil })
		if err != nil || state.EffectiveMode != "proxy-only" {
			t.Fatalf("managed DNS changed: %+v %v", state, err)
		}
	}
}

func TestDNSRestoreProbeFailureDoesNotMutateOwner(t *testing.T) {
	for _, at := range []string{"installed", "enabled", "running"} {
		t.Run(at, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "handoff.json")
			if err := writeDNSHandoff(path, dnsHandoffState{WasEnabled: true, WasRunning: true}); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("cannot inspect " + at)
			var probeErr error
			read := func(stage string) bool {
				if stage == at {
					probeErr = failure
					return stage == "installed"
				}
				return true
			}
			actions := 0
			ops := dnsHandoffOps{
				installed: func() bool { return read("installed") },
				enabled:   func() bool { return read("enabled") },
				running:   func() bool { return read("running") },
				err:       func() error { return probeErr },
				action:    func(string) error { actions++; return nil },
			}
			if err := restoreDNSHandoff(path, ops); !errors.Is(err, failure) || actions != 0 {
				t.Fatalf("uncertain owner was modified: actions=%d err=%v", actions, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("recovery marker lost", err)
			}
		})
	}
}

func TestDNSRestoreFinalProbeFailureRetainsCauseAndMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeDNSHandoff(path, dnsHandoffState{WasEnabled: true, WasRunning: true}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("verification status unavailable")
	enabled, running := false, false
	var probeErr error
	ops := dnsHandoffOps{
		installed: func() bool { return true },
		enabled:   func() bool { return enabled },
		running: func() bool {
			if running {
				probeErr = failure
				return false
			}
			return false
		},
		err: func() error { return probeErr },
		action: func(action string) error {
			if action == "enable" {
				enabled = true
			}
			if action == "start" {
				running = true
			}
			return nil
		},
	}
	if err := restoreDNSHandoff(path, ops); !errors.Is(err, failure) {
		t.Fatalf("verification error was replaced with a state mismatch: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("retry marker lost", err)
	}
}

func TestDNSHandoffWaitsForRedirectRemovalWithoutRestoringOwner(t *testing.T) {
	checks := 0
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	state, err := settleDNSInterop(ctx, "proxy-only", true,
		func(context.Context) (bool, error) { checks++; return checks == 1, nil },
		func(context.Context) (string, error) { return "", nil },
		func() error { t.Fatal("restored owner after its redirect disappeared"); return nil })
	if err != nil || checks != 2 || state.EffectiveMode != "proxy-only" {
		t.Fatalf("delayed firewall reload did not settle: checks=%d state=%+v err=%v", checks, state, err)
	}
}

func TestDNSHandoffCanceledWaitStillRestoresOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	failure := errors.New("restore failed")
	_, err := settleDNSInterop(ctx, "proxy-only", true,
		func(context.Context) (bool, error) { cancel(); return true, nil },
		func(context.Context) (string, error) { return "", nil },
		func() error { calls++; return failure })
	if calls != 1 || !errors.Is(err, context.Canceled) || !errors.Is(err, failure) {
		t.Fatalf("cancellation lost recovery or its error: calls=%d err=%v", calls, err)
	}
}
