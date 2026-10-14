package agentport

import (
	"errors"
	"fmt"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
	"time"
)

type ruleFallbackRuntime interface {
	ReconcileRuleFallback(string, bool) (bool, error)
}

// Only a completed health decision can authorize WAN. Startup, interrupted
// probes and local API failures do not. The main selector stays fail-closed.
func (runtime *xraySelectorRuntime) ReconcileRuleFallback(policy string, outage bool) (bool, error) {
	xray.MutationMu.Lock()
	defer xray.MutationMu.Unlock()
	tag := runtime.pool.HealthPolicies[policy].WANFallbackSelector
	if tag == "" {
		return false, nil
	}
	main, err := runtime.selectedRuntimeMember(policy)
	if err != nil {
		return false, err
	}
	if main == "" {
		return false, errors.New("route-list selection is unconfirmed")
	}
	member := main
	if main == "block" && outage {
		member = "direct-wan"
	}
	if err := runtime.setRuleFallbackMember(tag, member); err != nil {
		return false, err
	}
	return member == "direct-wan", nil
}

// Verify even when the primary selection is cached. Do not retire a shared
// handler before both selectors leave it. Confirm lost acknowledgements by readback.
func (runtime *xraySelectorRuntime) setRuleFallbackMember(tag, member string) error {
	actual, err := runtime.selectedRuntimeMember(tag)
	if err != nil {
		return err
	}
	if actual == member {
		return nil
	}
	if runtime.control != nil {
		err = runtime.control.override(runtime.requestContext(), tag, member)
	} else {
		_, err = runtime.command(runtime.requestContext(), 5*time.Second, runtime.opts.XrayBinary,
			"api", "bo", "--server="+runtime.opts.XrayAPIServer, "-b", tag, member)
	}
	confirmed, readErr := runtime.selectedRuntimeMember(tag)
	if readErr == nil && confirmed == member {
		return nil
	}
	return fmt.Errorf("rule fallback selector %q was not confirmed: %w", tag, errors.Join(err, readErr, errors.New("selector mismatch")))
}

func (runtime *responsiveSelectorRuntime) ReconcileRuleFallback(policy string, outage bool) (bool, error) {
	if fallback, ok := runtime.selectorRuntime.(ruleFallbackRuntime); ok {
		return fallback.ReconcileRuleFallback(policy, outage)
	}
	return false, errors.New("rule fallback runtime is unavailable")
}

// Selecting the same blocked primary during pool/DNS reconciliation must not
// revoke a WAN route already authorized by the live core. This never enables
// WAN: a fresh core, a transition from a proxy, or an inconsistent auxiliary
// selector still remains blocked until a completed health decision.
// The caller holds MutationMu.
func (runtime *xraySelectorRuntime) reconcileFallbackSelection(policy, tag, member string) error {
	if member == "block" {
		main, err := runtime.selectedRuntimeMember(policy)
		if err != nil {
			return err
		}
		runtime.selectorMembers[policy] = main
		if main == "block" {
			fallback, err := runtime.selectedRuntimeMember(tag)
			if err != nil {
				return err
			}
			if fallback == "direct-wan" {
				return nil
			}
		}
	}
	return runtime.setRuleFallbackMember(tag, member)
}
