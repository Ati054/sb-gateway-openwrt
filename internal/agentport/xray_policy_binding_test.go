package agentport

import (
	"encoding/json"
	"testing"
)

func TestPolicyBindingRefreshesEachSelectedHandlerOfSharedNode(t *testing.T) {
	for _, adopt := range []bool{false, true} {
		t.Run(map[bool]string{false: "Select", true: "CurrentAdoption"}[adopt], func(t *testing.T) {
			f, selectors := scopedProbeFixture(t)
			f.rt.pool.Policies = map[string][]string{"P": {"node"}, "Q": {"node"}}
			f.rt.pool.HealthPolicies = map[string]healthPolicyContract{"P": {Mode: "best", Candidates: []string{"node"}}, "Q": {Mode: "best", Candidates: []string{"node"}}}
			for _, policy := range []string{"P", "Q"} {
				if adopt {
					if got, e := f.rt.Current(policy); e != nil || got != "node" {
						t.Fatal(got, e)
					}
				} else {
					if e := f.rt.Select(policy, "node"); e != nil {
						t.Fatal(e)
					}
				}
			}
			oldP, oldQ := selectors["P"], selectors["Q"]
			f.rt.pool.Outbounds["node"] = json.RawMessage(`{"protocol":"freedom","settings":{"domainStrategy":"UseIPv4"}}`)
			// Refresh may switch the default policy first, before publishing
			// the pool to the health controller handling every other policy.
			if e := f.rt.Select("Q", "node"); e != nil {
				t.Fatal(e)
			}
			for _, policy := range []string{"P", "Q"} {
				got, e := f.rt.Current(policy)
				if e != nil || got != "node" {
					t.Fatalf("%s lost logical identity after refresh: %q %v", policy, got, e)
				}
				if selectors[policy] != f.rt.policyRuntimeTag(policy, "node") {
					t.Fatalf("%s did not reconcile its own new generation", policy)
				}
			}
			if !f.handlers[oldP] || !f.handlers[oldQ] {
				t.Fatal("refresh removed handlers carrying established streams")
			}
		})
	}
}

func TestPolicyBindingKeepsRemovedNodeUntilReplacementIsQualified(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	f.rt.pool.Policies = map[string][]string{"P": {"node"}}
	f.rt.pool.HealthPolicies = map[string]healthPolicyContract{"P": {Mode: "best", Candidates: []string{"node"}}}
	if e := f.rt.Select("P", "node"); e != nil {
		t.Fatal(e)
	}
	old := selectors["P"]
	creates := f.creates
	f.rt.pool.HealthPolicies["P"] = healthPolicyContract{Mode: "best"}
	got, e := f.rt.Current("P")
	if e != nil || got != "node" || selectors["P"] != old || f.creates != creates || !f.handlers[old] {
		t.Fatal("removed active handler was replaced before qualification", got, e)
	}
}
func TestPolicyBindingNeverAdoptsAnotherPolicyDynamicTag(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	f.rt.pool.Policies = map[string][]string{"P": {"node"}, "Q": {"node"}}
	f.rt.pool.HealthPolicies = map[string]healthPolicyContract{"P": {Mode: "best", Candidates: []string{"node"}}, "Q": {Mode: "best", Candidates: []string{"node"}}}
	for _, id := range []string{"P", "Q"} {
		if e := f.rt.Select(id, "node"); e != nil {
			t.Fatal(e)
		}
	}
	old := f.rt.policyBindings["P"]
	selectors["P"] = selectors["Q"]
	got, e := f.rt.Current("P")
	if e != nil || got != selectors["Q"] || f.rt.policyBindings["P"] != old {
		t.Fatal("foreign policy handler became owned evidence", got, e)
	}
}
func TestPolicyBindingRestartDoesNotGuessAnUnknownHistoricalTag(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	f.rt.pool.HealthPolicies = map[string]healthPolicyContract{"P": {Mode: "best", Candidates: []string{"node"}}}
	f.rt.pool.Outbounds["node"] = json.RawMessage(`{"protocol":"freedom","settings":{"domainStrategy":"UseIPv4"}}`)
	fresh := newXraySelectorRuntime(f.rt.opts)
	fresh.command = f.rt.command
	fresh.pool = f.rt.pool
	got, e := fresh.Current("P")
	if e != nil || got != selectors["P"] || len(fresh.policyBindings) != 0 || f.creates != 0 {
		t.Fatal("restart guessed an old tag binding or manufactured a live check", got, e)
	}
}

func TestPolicyBindingRetiresActualHandlerAfterExternalManagerAdoption(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	f.rt.pool.Policies = map[string][]string{"Q": {"node", "other"}}
	f.rt.pool.Outbounds["other"] = json.RawMessage(`{"protocol":"freedom","settings":{"domainStrategy":"UseIPv6"}}`)
	f.rt.pool.HealthPolicies = map[string]healthPolicyContract{"Q": {Mode: "best", Candidates: []string{"node", "other"}}}
	if e := f.rt.Select("Q", "node"); e != nil {
		t.Fatal(e)
	}
	old := selectors["Q"]
	f.rt.pool.Outbounds["node"] = json.RawMessage(`{"protocol":"freedom","settings":{"domainStrategy":"UseIPv4"}}`)
	actual := f.rt.policyRuntimeTag("Q", "node")
	// An independent manager changes only the core API, not this runtime's maps.
	if e := f.rt.ensureOutbound("node", actual); e != nil {
		t.Fatal(e)
	}
	selectors["Q"] = actual
	if got, e := f.rt.Current("Q"); e != nil || got != "node" {
		t.Fatal(got, e)
	}
	if f.rt.activeByPolicy["Q"] != old {
		t.Fatal("fixture no longer models stale agent ownership")
	}
	if e := f.rt.Select("Q", "other"); e != nil {
		t.Fatal(e)
	}
	if !contains(f.rt.retiredByPolicy["Q"], actual) || !f.handlers[actual] {
		t.Fatal("actual replaced handler was orphaned instead of retained for streams")
	}
	// A following transition retires the older owned generation in bounded cleanup.
	f.rt.pool.Outbounds["third"] = json.RawMessage(`{"protocol":"freedom","settings":{"domainStrategy":"AsIs"}}`)
	f.rt.pool.Policies["Q"] = append(f.rt.pool.Policies["Q"], "third")
	contract := f.rt.pool.HealthPolicies["Q"]
	contract.Candidates = append(contract.Candidates, "third")
	f.rt.pool.HealthPolicies["Q"] = contract
	if e := f.rt.Select("Q", "third"); e != nil {
		t.Fatal(e)
	}
	if f.handlers[actual] {
		t.Fatal("old adopted handler was never retired")
	}
}

func TestPolicyBindingKeepsActuallyDeletedDefinitionUntilQualifiedReplacement(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	f.rt.pool.Policies = map[string][]string{"P": {"node"}}
	f.rt.pool.HealthPolicies = map[string]healthPolicyContract{"P": {Mode: "best", Candidates: []string{"node"}}}
	if e := f.rt.Select("P", "node"); e != nil {
		t.Fatal(e)
	}
	old := selectors["P"]
	creates := f.creates
	delete(f.rt.pool.Outbounds, "node")
	f.rt.pool.HealthPolicies["P"] = healthPolicyContract{Mode: "best"}
	got, e := f.rt.Current("P")
	if e != nil || got != "node" || selectors["P"] != old || f.creates != creates || !f.handlers[old] {
		t.Fatal("deleted definition caused an unqualified replacement", got, e)
	}
}
