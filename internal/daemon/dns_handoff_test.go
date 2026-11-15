package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDNSHandoffRequiresConfirmationAndRestoresService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	running, enabled := true, true
	actions := []string{}
	ops := dnsHandoffOps{
		installed: func() bool { return true },
		running:   func() bool { return running },
		enabled:   func() bool { return enabled },
		action: func(action string) error {
			actions = append(actions, action)
			switch action {
			case "stop":
				running = false
			case "start":
				running = true
			case "disable":
				enabled = false
			case "enable":
				enabled = true
			default:
				return errors.New("unexpected action")
			}
			return nil
		},
	}
	if acquired, err := acquireDNSHandoff(path, false, ops); acquired || err != nil || !running || !enabled {
		t.Fatalf("unconfirmed takeover changed service: acquired=%v err=%v", acquired, err)
	}
	if acquired, err := acquireDNSHandoff(path, true, ops); !acquired || err != nil || running || enabled {
		t.Fatalf("confirmed takeover failed: acquired=%v err=%v", acquired, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("takeover marker missing: %v", err)
	}
	if err := restoreDNSHandoff(path, ops); err != nil || !running || !enabled {
		t.Fatalf("service state not restored: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("takeover marker was not removed: %v", err)
	}
	if len(actions) != 4 || actions[0] != "stop" || actions[1] != "disable" || actions[2] != "enable" || actions[3] != "start" {
		t.Fatalf("unexpected service actions: %v", actions)
	}
}

func TestDNSHandoffLeavesStoppedInstalledServiceUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	ops := dnsHandoffOps{
		installed: func() bool { return true },
		running:   func() bool { return false },
		enabled:   func() bool { return false },
		action:    func(string) error { t.Fatal("stopped service was modified"); return nil },
	}
	if acquired, err := acquireDNSHandoff(path, true, ops); acquired || err != nil {
		t.Fatalf("stopped service was acquired: %v %v", acquired, err)
	}
}

func TestDNSHandoffFailedStopRollsBackMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	ops := dnsHandoffOps{
		installed: func() bool { return true },
		running:   func() bool { return true },
		enabled:   func() bool { return true },
		action: func(action string) error {
			if action == "stop" {
				return errors.New("stop failed")
			}
			return nil
		},
	}
	if acquired, err := acquireDNSHandoff(path, true, ops); acquired || err == nil {
		t.Fatalf("failed stop was accepted: acquired=%v err=%v", acquired, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed takeover left a marker: %v", err)
	}
}

func TestDNSRestoreReadFailureRetainsMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeDNSHandoff(path, dnsHandoffState{}); err != nil {
		t.Fatal(err)
	}
	ops := dnsHandoffOps{installed: func() bool { return true }, running: func() bool { return false }, enabled: func() bool { return false }, action: func(string) error { return nil }, err: func() error { return context.DeadlineExceeded }}
	if err := restoreDNSHandoff(path, ops); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("read failure ignored", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("recovery marker discarded", err)
	}
}

func TestDNSHandoffStatusFailureAfterMutationRestoresOwner(t *testing.T) {
	for _, failAfter := range []string{"stop", "disable"} {
		t.Run(failAfter, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "handoff.json")
			running, enabled := true, true
			var statusErr error
			failure := errors.New("status probe failed after " + failAfter)
			action := func(action string) error {
				switch action {
				case "stop":
					running = false
				case "start":
					running = true
				case "disable":
					enabled = false
				case "enable":
					enabled = true
				}
				if action == failAfter {
					statusErr = failure
				}
				return nil
			}
			ops := dnsHandoffOps{
				installed: func() bool { return true },
				running:   func() bool { return running },
				enabled:   func() bool { return enabled },
				action:    action,
				err:       func() error { return statusErr },
				recovery: func() (dnsHandoffOps, context.CancelFunc) {
					return dnsHandoffOps{
						installed: func() bool { return true },
						running:   func() bool { return running },
						enabled:   func() bool { return enabled },
						action:    action,
					}, func() {}
				},
			}
			acquired, err := acquireDNSHandoff(path, true, ops)
			if acquired || !errors.Is(err, failure) || !running || !enabled {
				t.Fatalf("owner not restored: acquired=%v running=%v enabled=%v err=%v", acquired, running, enabled, err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("verified recovery left a stale marker: %v", err)
			}
		})
	}
}

func TestDNSHandoffFailedRecoveryKeepsMarkerAndBothErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeDNSHandoff(path, dnsHandoffState{WasEnabled: true, WasRunning: true}); err != nil {
		t.Fatal(err)
	}
	failure, recoveryFailure := errors.New("status failed"), errors.New("recovery failed")
	ops := dnsHandoffOps{
		installed: func() bool { return true }, running: func() bool { return false },
		enabled: func() bool { return false }, action: func(string) error { return recoveryFailure },
		err: func() error { return failure },
		recovery: func() (dnsHandoffOps, context.CancelFunc) {
			return dnsHandoffOps{
				installed: func() bool { return true }, running: func() bool { return false },
				enabled: func() bool { return false }, action: func(string) error { return recoveryFailure },
			}, func() {}
		},
	}
	acquired, err := acquireDNSHandoff(path, true, ops)
	if acquired || !errors.Is(err, failure) || !errors.Is(err, recoveryFailure) {
		t.Fatalf("failed recovery was masked: %v, %v", acquired, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed recovery discarded marker", err)
	}
}
