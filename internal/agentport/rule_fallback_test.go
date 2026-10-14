package agentport

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
	"testing"
	"time"
)

func TestRuleFallbackRuntimeOutageRecoveryAndLostAcknowledgement(t *testing.T) {
	main, aux := "resproxy-main", xray.WANFallbackTag("main")
	for _, lost := range []bool{false, true} {
		rt := newXraySelectorRuntime(Options{})
		rt.pool = healthPool{Policies: map[string][]string{main: {"node"}}, HealthPolicies: map[string]healthPolicyContract{main: {WANFallbackSelector: aux}}}
		selected := map[string]string{main: "block", aux: "block"}
		rt.command = func(_ context.Context, _ time.Duration, _ string, args ...string) ([]byte, error) {
			if args[1] == "bo" {
				selected[args[len(args)-2]] = args[len(args)-1]
				if lost {
					return nil, errors.New("lost acknowledgement")
				}
				return nil, nil
			}
			return selectorInfo(selected[args[len(args)-1]]), nil
		}
		active, err := rt.ReconcileRuleFallback(main, false)
		if err != nil || active || selected[aux] != "block" {
			t.Fatalf("startup authorized WAN: %v %v", active, err)
		}
		active, err = rt.ReconcileRuleFallback(main, true)
		if err != nil || !active || selected[main] != "block" || selected[aux] != "direct-wan" {
			t.Fatalf("outage: %v %v %v", active, err, selected)
		}
		selected[main] = "confirmed-proxy-leaf"
		active, err = rt.ReconcileRuleFallback(main, true)
		if err != nil || active || selected[aux] != selected[main] {
			t.Fatalf("recovery: %v %v %v", active, err, selected)
		}
	}
}

func TestRuleFallbackUpdateFailureDoesNotCommitOrRetireProxyLeaf(t *testing.T) {
	main, aux := "resproxy-main", xray.WANFallbackTag("main")
	rt := newXraySelectorRuntime(Options{})
	rt.pool = healthPool{Policies: map[string][]string{main: {"node"}}, HealthPolicies: map[string]healthPolicyContract{main: {WANFallbackSelector: aux}}}
	rt.activeByPolicy[main] = "old-proxy-leaf"
	selected := map[string]string{main: "old-proxy-leaf", aux: "old-proxy-leaf"}
	rt.command = func(_ context.Context, _ time.Duration, _ string, args ...string) ([]byte, error) {
		if args[1] == "bo" {
			return nil, errors.New("API rejected override")
		}
		return selectorInfo(selected[args[len(args)-1]]), nil
	}
	if rt.Select(main, "block") == nil {
		t.Fatal("unconfirmed auxiliary update accepted")
	}
	if rt.activeByPolicy[main] != "old-proxy-leaf" || len(rt.retiredByPolicy[main]) != 0 || selected[main] != "old-proxy-leaf" {
		t.Fatal("failed update retired a still-referenced handler")
	}
}

type fallbackTestRuntime struct {
	fakeSelectorRuntime
	calls       []bool
	fallbackErr error
}

func (r *fallbackTestRuntime) ReconcileRuleFallback(_ string, outage bool) (bool, error) {
	r.calls = append(r.calls, outage)
	return outage && r.fallbackErr == nil, r.fallbackErr
}

func TestRuleFallbackControllerEmptyMembershipAndRuntimeFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		pool := healthFixture(true)
		c := pool.HealthPolicies["europe"]
		c.Candidates = nil
		c.WANFallbackSelector = xray.WANFallbackTag("europe")
		pool.HealthPolicies["europe"] = c
		rt := &fallbackTestRuntime{fakeSelectorRuntime: fakeSelectorRuntime{pool: pool, current: map[string]string{"europe": "removed-node"}}}
		if fail {
			rt.fallbackErr = errors.New("API unavailable")
		}
		controller := &healthController{opts: Options{StateRoot: t.TempDir()}, runtime: rt}
		err := controller.Tick(time.Now())
		item := controller.state["europe"]
		if fail {
			if err == nil || item.RuntimeConfirmed || item.WANFallbackActive {
				t.Fatal("failed runtime marked WAN confirmed")
			}
		} else if err != nil || !item.RuntimeConfirmed || !item.WANFallbackActive || item.Selected != "block" || item.CandidateCount != 0 {
			t.Fatalf("empty membership: %v %+v", err, item)
		}
		if !fail {
			before := len(rt.selections)
			if err := controller.Tick(time.Now().Add(4 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if len(rt.selections) != before || !item.WANFallbackActive {
				t.Fatal("stable empty route list interrupted confirmed WAN fallback")
			}
		}
	}
}

func TestRuleFallbackRestoredStateCannotAuthorizeWAN(t *testing.T) {
	var item policyHealthState
	if err := json.Unmarshal([]byte(`{"selected":"block","runtime_confirmed":true,"wan_fallback_active":true}`), &item); err != nil {
		t.Fatal(err)
	}
	if item.WANFallbackActive {
		t.Fatal("saved state authorized WAN without live proof")
	}
}

func TestRuleFallbackControllerProvenFailureAndRecovery(t *testing.T) {
	pool := healthFixture(true)
	c := pool.HealthPolicies["europe"]
	c.WANFallbackSelector = xray.WANFallbackTag("europe")
	pool.HealthPolicies["europe"] = c
	rt := &fallbackTestRuntime{fakeSelectorRuntime: fakeSelectorRuntime{pool: pool, current: map[string]string{"europe": "de"}, probes: map[string]probeEvidence{"de": {Failure: probeFailureFatal}, "nl": {Failure: probeFailureFatal}}}}
	controller := &healthController{opts: Options{StateRoot: t.TempDir()}, runtime: rt}
	now := time.Now()
	for i := 0; i < 5; i++ {
		if err := controller.Tick(now.Add(time.Duration(i) * 20 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	item := controller.state["europe"]
	if item.Selected != "block" || !item.WANFallbackActive {
		t.Fatalf("outage did not authorize per-rule WAN: selected=%s active=%v", item.Selected, item.WANFallbackActive)
	}
	rt.probes["de"] = successfulEvidence(100)
	for i := 5; i < 8; i++ {
		if err := controller.Tick(now.Add(time.Duration(i) * 20 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if item.Selected != "de" || item.WANFallbackActive {
		t.Fatalf("recovery did not revoke WAN: selected=%s active=%v", item.Selected, item.WANFallbackActive)
	}
}
