package agentport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func preferredFixture(mode string) (healthPool, healthState) {
	pool := healthFixture(true)
	contract := pool.HealthPolicies["europe"]
	contract.Mode = mode
	contract.Policy.ActiveCheckSeconds = 60
	node := contract.Nodes["nl"]
	node.Fingerprint = strings.Repeat("a", 64)
	contract.Nodes["nl"] = node
	pool.HealthPolicies["europe"] = contract
	item := newPolicyHealthState()
	ensureHealthMaps(item)
	ensureHealthMaps(item)
	item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "nl", "nl", true
	item.Mode, item.CandidateSignature = mode, "de\nnl"
	item.CandidateNodes = contract.Nodes
	item.AvailabilityOK = map[string]bool{"nl": true}
	item.Samples["nl"] = []healthSample{{At: 1000, OK: true}}
	rememberWorkingSelection(contract, item)
	return pool, healthState{"europe": item}
}

type coldPreferenceRuntime struct {
	*fakeSelectorRuntime
	t *testing.T
}

func (runtime *coldPreferenceRuntime) Select(selector, member string) error {
	if member == "nl" && !contains(runtime.availabilityCalls, "nl") {
		runtime.t.Fatal("saved preference was admitted without a fresh availability probe")
	}
	return runtime.fakeSelectorRuntime.Select(selector, member)
}

func TestColdBootPreferredServerNeedsFreshProof(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		t.Run(mode, func(t *testing.T) {
			pool, state := preferredFixture(mode)
			file := filepath.Join(t.TempDir(), "preferred.json")
			writer := &healthController{opts: Options{PreferredSelectionsFile: file}}
			writer.persistPreferredSelections(state, time.Unix(1000, 0))
			poolFile := filepath.Join(t.TempDir(), "pool.json")
			if err := writeJSONAtomic(poolFile, pool); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir() // /var/run is empty after a router reboot.
			if err := RestoreColdSelections(root, file, poolFile); err != nil {
				t.Fatal(err)
			}
			var restored healthState
			if err := readJSON(statePath(root, "selector-health"), &restored); err != nil {
				t.Fatal(err)
			}
			item := restored["europe"]
			if item == nil || item.Selected != "nl" || item.RuntimeConfirmed || item.RuntimeSelected != "" ||
				len(item.Samples) != 0 || len(item.DailySamples) != 0 || len(item.AvailabilityOK) != 0 || len(item.LastGoodAt) != 0 {
				t.Fatalf("cold preference carried live proof/history: %+v", item)
			}
			runtime := &coldPreferenceRuntime{&fakeSelectorRuntime{pool: pool, reset: true,
				current: map[string]string{"europe": "block"},
				probes:  map[string]probeEvidence{"de": successfulEvidence(5), "nl": successfulEvidence(100)}}, t}
			controller := &healthController{opts: Options{StateRoot: root, PreferredSelectionsFile: file}, runtime: runtime}
			if err := controller.Tick(time.Unix(2000, 0)); err != nil {
				t.Fatal(err)
			}
			if len(runtime.selections) == 0 || runtime.selections[0] != [2]string{"europe", "nl"} {
				t.Fatalf("cold boot raced another server: %v", runtime.selections)
			}
		})
	}
}

func TestColdPreferenceRejectsChangedOrRemovedServer(t *testing.T) {
	for _, change := range []string{"fingerprint", "mode", "membership", "priority-order", "removed-policy"} {
		t.Run(change, func(t *testing.T) {
			pool, state := preferredFixture("priority")
			file := filepath.Join(t.TempDir(), "preferred.json")
			(&healthController{opts: Options{PreferredSelectionsFile: file}}).persistPreferredSelections(state, time.Unix(1000, 0))
			contract := pool.HealthPolicies["europe"]
			switch change {
			case "fingerprint":
				node := contract.Nodes["nl"]
				node.Fingerprint = strings.Repeat("b", 64)
				contract.Nodes["nl"] = node
			case "mode":
				contract.Mode = "best"
			case "membership":
				contract.Candidates = []string{"de"}
			case "priority-order":
				contract.Candidates = []string{"nl", "de"}
			}
			pool.HealthPolicies["europe"] = contract
			if change == "removed-policy" {
				delete(pool.HealthPolicies, "europe")
			}
			poolFile := filepath.Join(t.TempDir(), "pool.json")
			if err := writeJSONAtomic(poolFile, pool); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := RestoreColdSelections(root, file, poolFile); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(statePath(root, "selector-health")); !os.IsNotExist(err) {
				t.Fatalf("obsolete preference survived %s: %v", change, err)
			}
		})
	}
}

func TestColdBootUnreachablePreferenceNeverReceivesTraffic(t *testing.T) {
	pool, state := preferredFixture("best")
	file := filepath.Join(t.TempDir(), "preferred.json")
	(&healthController{opts: Options{PreferredSelectionsFile: file}}).persistPreferredSelections(state, time.Unix(1000, 0))
	poolFile := filepath.Join(t.TempDir(), "pool.json")
	if err := writeJSONAtomic(poolFile, pool); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := RestoreColdSelections(root, file, poolFile); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeSelectorRuntime{pool: pool, reset: true, current: map[string]string{"europe": "block"},
		probes: map[string]probeEvidence{"de": successfulEvidence(10), "nl": failedEvidence()}}
	controller := &healthController{opts: Options{StateRoot: root}, runtime: runtime}
	if err := controller.Tick(time.Unix(2000, 0)); err != nil {
		t.Fatal(err)
	}
	for _, selected := range runtime.selections {
		if selected[1] == "nl" {
			t.Fatal("unreachable remembered server admitted traffic")
		}
	}
	if !contains(runtime.availabilityCalls, "nl") {
		t.Fatal("remembered server was not checked")
	}
	if runtime.current["europe"] != "de" {
		t.Fatal("working reserve not selected", runtime.current)
	}
}

func TestPreferredServersDoNotWriteProbeHistoryOrRepeatedFlashUpdates(t *testing.T) {
	_, state := preferredFixture("best")
	file := filepath.Join(t.TempDir(), "preferred.json")
	controller := &healthController{opts: Options{PreferredSelectionsFile: file}}
	controller.persistPreferredSelections(state, time.Unix(1000, 0))
	first, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	state["europe"].Samples["nl"] = append(state["europe"].Samples["nl"], healthSample{At: 2000, OK: false})
	state["europe"].RuntimeConfirmed = false
	state["europe"].AvailabilityFailures["nl"] = 3
	controller.persistPreferredSelections(state, time.Unix(2000, 0))
	second, err := os.Stat(file)
	if err != nil || !os.SameFile(first, second) {
		t.Fatal("unchanged server rewrote flash", err)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"samples", "failures", "availability_ok", "runtime_confirmed", "runtime_selected"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal("persisted live evidence/history", forbidden)
		}
	}
	// A service restart also leaves an identical existing preference untouched.
	(&healthController{opts: Options{PreferredSelectionsFile: file}}).persistPreferredSelections(state, time.Unix(3000, 0))
	third, err := os.Stat(file)
	if err != nil || !os.SameFile(first, third) {
		t.Fatal("restart rewrote unchanged flash", err)
	}
}

func TestColdPreferenceLeavesWarmStateAndRejectsCorruption(t *testing.T) {
	pool, state := preferredFixture("best")
	file := filepath.Join(t.TempDir(), "preferred.json")
	(&healthController{opts: Options{PreferredSelectionsFile: file}}).persistPreferredSelections(state, time.Unix(1000, 0))
	root := t.TempDir()
	path := statePath(root, "selector-health")
	if err := writeJSONAtomic(path, healthState{"live": newPolicyHealthState()}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := RestoreColdSelections(root, file, "/missing-pool"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("cold preference overwrote warm state")
	}
	poolFile := filepath.Join(t.TempDir(), "pool.json")
	if err := writeJSONAtomic(poolFile, pool); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"null", "{bad", `{"europe":{"selected":"nl","mode":"best","candidate_signature":"nl","fingerprint":"bad"}}`, strings.Repeat("x", preferredSelectionsMaxBytes+1)} {
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := RestoreColdSelections(t.TempDir(), file, poolFile); err == nil {
			t.Fatal("corrupt preference accepted")
		}
	}
}

func TestPreferredFlashFailureDoesNotStopPublishingAndRetriesAreBounded(t *testing.T) {
	_, state := preferredFixture("best")
	parent := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(parent, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(parent, "preferred.json")
	controller := &healthController{opts: Options{PreferredSelectionsFile: file}}
	path := statePath(t.TempDir(), "selector-health")
	now := time.Unix(1000, 0)
	if err := controller.publishState(path, state, now); err != nil {
		t.Fatal("flash error stopped RAM publication", err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := controller.publishState(path, state, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("flash retry was not deferred", err)
	}
	if err := controller.publishState(path, state, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("latest preference not saved after retry", err)
	}
}
