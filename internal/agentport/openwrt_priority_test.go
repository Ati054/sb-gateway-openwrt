package agentport

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type reversedOpenWrtRuntime struct{ *fakeSelectorRuntime }

func (r *reversedOpenWrtRuntime) ProbeEmergencyAvailabilityParallel(ids []string, cb func(string, probeEvidence) bool) map[string]probeEvidence {
	out := map[string]probeEvidence{}
	for i := len(ids) - 1; i >= 0; i-- {
		id := ids[i]
		out[id] = r.probes[id]
		if cb(id, out[id]) {
			break
		}
	}
	return out
}
func TestOpenWrtPriorityWaitsForEarlierParallelResults(t *testing.T) {
	for _, firstOK := range []bool{true, false} {
		evidence := failedEvidence()
		want := "fr2"
		if firstOK {
			evidence = successfulEvidence(700)
			want = "fr1"
		}
		runtime := &reversedOpenWrtRuntime{&fakeSelectorRuntime{probes: map[string]probeEvidence{"fr1": evidence, "fr2": successfulEvidence(600), "pl": successfulEvidence(20)}}}
		c := &healthController{runtime: runtime}
		measured, selected, err := c.probeEmergencyCandidates("pc", []string{"fr1", "fr2", "pl"}, effectivePolicySettings{mode: "priority"})
		if err != nil || selected != want || len(measured) != 3 || len(runtime.selections) != 1 {
			t.Fatalf("parallel priority violated: selected=%s measured=%v err=%v", selected, measured, err)
		}
	}
}
func TestOpenWrtRestartRestoresWorkingSelectionAndDatedHistory(t *testing.T) {
	now := time.Unix(2000000000, 0)
	contract := healthPolicyContract{Mode: "best", Candidates: []string{"a", "b"}, Policy: healthPolicy{LatencyMeasurement: "https-head-v1"}, Nodes: map[string]healthNode{"a": {Fingerprint: "fp-a"}, "b": {Fingerprint: "fp-b"}}}
	item := newPolicyHealthState()
	ensureHealthMaps(item)
	item.Mode = "best"
	item.Selected = "b"
	item.RuntimeSelected = "b"
	item.RuntimeConfirmed = true
	item.LatencyMeasurement = "https-head-v1"
	item.CandidateSignature = strings.Join(contract.Candidates, "\n")
	item.CandidateNodes = contract.Nodes
	item.AvailabilityOK = map[string]bool{"b": true}
	item.QualityOK = map[string]bool{"b": true}
	item.Recoveries["b"] = 3
	item.LastWorkingSelection = &workingSelection{"b", "best", item.CandidateSignature}
	day := now.UTC().Format("2006-01-02")
	item.HistoryDays["b"] = map[string]dayBucket{day: {Samples: 42, Successes: 42, LatencySamples: []int{100, 200}}}
	root := t.TempDir()
	if err := writeJSONAtomic(statePath(root, "selector-health"), healthState{"all": item}); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeSelectorRuntime{pool: healthPool{ProbeBudget: 10, HealthPolicies: map[string]healthPolicyContract{"all": contract}}, current: map[string]string{"all": "block"}, probes: map[string]probeEvidence{"a": successfulEvidence(20), "b": successfulEvidence(80)}}
	c := &healthController{runtime: runtime, opts: Options{StateRoot: root, HealthInterval: time.Minute}, warmStarted: map[string]bool{}}
	if err := c.Tick(now); err != nil {
		t.Fatal(err)
	}
	got := c.state["all"]
	if got.Selected != "b" || got.HistoryDays["b"][day].Samples < 42 || len(runtime.availabilityCalls) == 0 || runtime.availabilityCalls[0] != "b" {
		t.Fatalf("restart lost selected path/history: selected=%s history=%v calls=%v", got.Selected, got.HistoryDays, runtime.availabilityCalls)
	}
}
func TestOpenWrtEndpointChangeKeepsHistoryButClearsCurrentEvidence(t *testing.T) {
	item := newPolicyHealthState()
	ensureHealthMaps(item)
	item.CandidateNodes = map[string]healthNode{"node": {Fingerprint: "old"}}
	item.LastProbeAt["node"] = 10
	item.HistoryDays["node"] = map[string]dayBucket{"2026-10-08": {Samples: 42, Successes: 42}}
	item.AvailabilityOK = map[string]bool{"node": true}
	changed := invalidateChangedOutboundHealth(item, []string{"node"}, map[string]healthNode{"node": {Fingerprint: "new"}})
	if len(changed) != 1 || item.AvailabilityOK["node"] || item.LastProbeAt["node"] != 0 || item.HistoryDays["node"]["2026-10-08"].Samples != 42 {
		t.Fatal("endpoint change lost dated history or reused stale health")
	}
}
func TestOpenWrtLegacySpeedStateMigratesWithoutDownloadPolicy(t *testing.T) {
	var item policyHealthState
	if err := json.Unmarshal([]byte(`{"selected":"node","history_days":{"node":{"2026-10-08":{"samples":42}}},"speed_samples_bps":{"node":[100]},"optimization_candidate":"other","optimization_checks":3}`), &item); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if item.Selected != "node" || item.OptimizationChecks != 0 || item.HistoryDays["node"]["2026-10-08"].Samples != 42 || strings.Contains(string(body), "speed_") {
		t.Fatal("legacy speed state did not migrate safely")
	}
}

func TestOpenWrtPriorityBatchIgnoresTCPAndCachedReservePromotion(t *testing.T) {
	now := time.Unix(1000, 0)
	item := newPolicyHealthState()
	item.Selected = "block"
	runtime := &preflightFixture{fakeSelectorRuntime: &fakeSelectorRuntime{}, open: "pl"}
	c := &healthController{runtime: runtime}
	p := effectivePolicySettings{mode: "priority", batch: 2, blockRecovery: 15, backup: 15}
	got := c.emergencyTargets(now, []string{"fr1", "fr2", "pl", "gb"}, item, p)
	if strings.Join(got, ",") != "fr1,fr2" {
		t.Fatal("TCP endpoint jumped ordered prefix", got)
	}
	item.LastProbeAt["fr1"] = 1000
	item.LastProbeAt["fr2"] = 1000
	got = c.emergencyTargets(now.Add(time.Second), []string{"fr1", "fr2", "pl", "gb"}, item, p)
	if strings.Join(got, ",") != "pl,gb" {
		t.Fatal("failed prefix starved later nodes", got)
	}
}
