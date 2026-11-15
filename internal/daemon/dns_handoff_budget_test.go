package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDNSHandoffRecoverySharesDeadlineAndRetainsMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeDNSHandoff(path, dnsHandoffState{WasEnabled: true, WasRunning: true}); err != nil {
		t.Fatal(err)
	}
	steps := 0
	started := time.Now()
	err := restoreDNSHandoffWithinBudget(path, 150*time.Millisecond, func(ctx context.Context) dnsHandoffOps {
		var failure error
		step := func() error {
			steps++
			timer := time.NewTimer(60 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				failure = ctx.Err()
			case <-timer.C:
			}
			return failure
		}
		return dnsHandoffOps{
			installed: func() bool { return true },
			enabled:   func() bool { _ = step(); return false },
			running:   func() bool { _ = step(); return false },
			action:    func(string) error { return step() },
			err:       func() error { return failure },
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) || steps < 2 || steps > 3 || time.Since(started) > time.Second {
		t.Fatalf("recovery reset its overall deadline: steps=%d elapsed=%s err=%v", steps, time.Since(started), err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unfinished restoration discarded its recovery marker: %v", err)
	}
}

func TestDNSHandoffRecoverySurvivesCanceledStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeDNSHandoff(path, dnsHandoffState{WasEnabled: true, WasRunning: true}); err != nil {
		t.Fatal(err)
	}
	startup, cancel := context.WithCancel(t.Context())
	cancel()
	enabled, running := false, false
	_, err := settleDNSInterop(startup, "proxy-only", true,
		func(context.Context) (bool, error) { return false, context.Canceled },
		func(context.Context) (string, error) { return "", context.Canceled },
		func() error {
			return restoreDNSHandoffWithinBudget(path, time.Second, func(ctx context.Context) dnsHandoffOps {
				if ctx.Err() != nil {
					t.Fatal("startup cancellation prevented owner recovery")
				}
				return dnsHandoffOps{
					installed: func() bool { return true },
					enabled:   func() bool { return enabled },
					running:   func() bool { return running },
					action: func(action string) error {
						if action == "enable" {
							enabled = true
						}
						if action == "start" {
							running = true
						}
						return nil
					},
					err: ctx.Err,
				}
			})
		})
	if !errors.Is(err, context.Canceled) || !enabled || !running {
		t.Fatalf("canceled startup lost owner recovery: enabled=%v running=%v err=%v", enabled, running, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified restoration retained stale marker: %v", err)
	}
}
