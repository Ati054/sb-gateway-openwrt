package agentport

import (
	"errors"
	"example.org/resilient-proxy/internal/xray"
	"fmt"
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
