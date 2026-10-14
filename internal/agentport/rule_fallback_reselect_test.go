package agentport

import (
	"context"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
	"testing"
	"time"
)

func TestBlockedSelectorReconciliationPreservesLiveWAN(t *testing.T) {
	for _, cached := range []bool{false, true} {
		main, aux := "resproxy-main", xray.WANFallbackTag("main")
		rt := newXraySelectorRuntime(Options{})
		rt.pool = healthPool{Policies: map[string][]string{main: {"node"}}, HealthPolicies: map[string]healthPolicyContract{main: {WANFallbackSelector: aux}}}
		selected := map[string]string{main: "block", aux: "direct-wan"}
		if cached {
			rt.selectorMembers[main] = "block"
		}
		mutations := 0
		rt.command = func(_ context.Context, _ time.Duration, _ string, args ...string) ([]byte, error) {
			if args[1] == "bo" {
				mutations++
				selected[args[len(args)-2]] = args[len(args)-1]
				return nil, nil
			}
			return selectorInfo(selected[args[len(args)-1]]), nil
		}
		if err := rt.Select(main, "block"); err != nil {
			t.Fatal(err)
		}
		if selected[aux] != "direct-wan" || mutations != 0 {
			t.Fatalf("warm reconciliation interrupted authorized WAN: cached=%v mutations=%d selectors=%v", cached, mutations, selected)
		}
	}
}

func TestTransitionToBlockedSelectorDoesNotAuthorizeWAN(t *testing.T) {
	main, aux := "resproxy-main", xray.WANFallbackTag("main")
	rt := newXraySelectorRuntime(Options{})
	rt.pool = healthPool{Policies: map[string][]string{main: {"node"}}, HealthPolicies: map[string]healthPolicyContract{main: {WANFallbackSelector: aux}}}
	selected := map[string]string{main: "old-proxy-leaf", aux: "old-proxy-leaf"}
	rt.command = func(_ context.Context, _ time.Duration, _ string, args ...string) ([]byte, error) {
		if args[1] == "bo" {
			selected[args[len(args)-2]] = args[len(args)-1]
			return nil, nil
		}
		return selectorInfo(selected[args[len(args)-1]]), nil
	}
	if err := rt.Select(main, "block"); err != nil {
		t.Fatal(err)
	}
	if selected[main] != "block" || selected[aux] != "block" {
		t.Fatal("unconfirmed failure enabled WAN")
	}
}

func TestBlockedReselectionRequiresLiveWANEvidence(t *testing.T) {
	for _, tc := range []struct{ name, main, aux string }{
		{"new core", "block", "block"},
		{"stale primary cache", "old-proxy-leaf", "direct-wan"},
		{"inconsistent auxiliary", "block", "old-proxy-leaf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main, aux := "resproxy-main", xray.WANFallbackTag("main")
			rt := newXraySelectorRuntime(Options{})
			rt.pool = healthPool{Policies: map[string][]string{main: {"node"}}, HealthPolicies: map[string]healthPolicyContract{main: {WANFallbackSelector: aux}}}
			rt.selectorMembers[main] = "block"
			selected := map[string]string{main: tc.main, aux: tc.aux}
			rt.command = func(_ context.Context, _ time.Duration, _ string, args ...string) ([]byte, error) {
				if args[1] == "bo" {
					selected[args[len(args)-2]] = args[len(args)-1]
					return nil, nil
				}
				return selectorInfo(selected[args[len(args)-1]]), nil
			}
			if err := rt.Select(main, "block"); err != nil {
				t.Fatal(err)
			}
			if selected[main] != "block" || selected[aux] != "block" {
				t.Fatalf("WAN retained without coherent live proof: %v", selected)
			}
		})
	}
}
