package agentport

import (
	"path/filepath"
	"testing"
	"time"
)

type startupSurveyFixture struct {
	*fakeSelectorRuntime
	t    *testing.T
	root string
}

func (r *startupSurveyFixture) ProbeAvailability(candidate string) probeEvidence {
	var state healthState
	if err := readJSON(filepath.Join(r.root, "selector-health.json"), &state); err != nil {
		r.t.Fatal(err)
	}
	for id, item := range state {
		if r.current[id] == "block" && item.RuntimeConfirmed && item.RuntimeSelected != "block" {
			r.t.Fatalf("old core proof published for unrestored sheet %s", id)
		}
	}
	return r.fakeSelectorRuntime.ProbeAvailability(candidate)
}

func (r *startupSurveyFixture) Probe(candidate string) probeEvidence {
	for _, id := range []string{"all", "pc"} {
		if r.current[id] != id+"-node" {
			r.t.Fatalf("quality survey %s started while remembered route %s still selected %q", candidate, id, r.current[id])
		}
	}
	return r.fakeSelectorRuntime.Probe(candidate)
}

func TestRestartRestoresEveryRememberedRouteBeforeQualitySurvey(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		t.Run(mode, func(t *testing.T) {
			base := healthFixture(false).HealthPolicies["europe"]
			pool := healthPool{HealthPolicies: map[string]healthPolicyContract{}}
			state := healthState{}
			runtime := &startupSurveyFixture{fakeSelectorRuntime: &fakeSelectorRuntime{
				pool: pool, reset: true, current: map[string]string{}, probes: map[string]probeEvidence{},
			}, t: t}
			for _, id := range []string{"all", "pc"} {
				contract := base
				contract.Mode, contract.Candidates = mode, []string{id + "-node"}
				pool.HealthPolicies[id] = contract
				item := newPolicyHealthState()
				ensureHealthMaps(item)
				item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = id+"-node", id+"-node", true
				item.Mode, item.CandidateSignature = mode, id+"-node"
				item.AvailabilityOK = map[string]bool{id + "-node": true}
				item.LastWorkingSelection = &workingSelection{id + "-node", mode, id + "-node"}
				item.Samples[id+"-node"] = []healthSample{{OK: true}}
				state[id] = item
				runtime.current[id] = "block"
				runtime.probes[id+"-node"] = successfulEvidence(20)
			}
			root := t.TempDir()
			runtime.root = root
			if err := writeJSONAtomic(filepath.Join(root, "selector-health.json"), state); err != nil {
				t.Fatal(err)
			}
			controller := &healthController{opts: Options{StateRoot: root}, runtime: runtime}
			if err := controller.Tick(time.Unix(2000, 0)); err != nil {
				t.Fatal(err)
			}
			if len(runtime.probeCalls) == 0 {
				t.Fatal("fixture did not run quality survey")
			}
		})
	}
}

func TestRestartInvalidatesRuntimeProofAndPreservesHistory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "selector-health.json")
	item := newPolicyHealthState()
	item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "node", "node", true
	item.Mode, item.CandidateSignature = "best", "node"
	item.AvailabilityOK = map[string]bool{"node": true}
	item.WANFallbackActive = true
	item.Samples["node"] = []healthSample{{OK: true}}
	if err := writeJSONAtomic(path, healthState{"route": item}); err != nil {
		t.Fatal(err)
	}
	if err := InvalidateRuntimeHealth(root); err != nil {
		t.Fatal(err)
	}
	var state healthState
	if err := readJSON(path, &state); err != nil {
		t.Fatal(err)
	}
	got := state["route"]
	if got.RuntimeConfirmed || got.RuntimeSelected != "" || got.WANFallbackActive {
		t.Fatalf("old runtime proof survived restart: %+v", got)
	}
	if got.Selected != "node" || got.LastWorkingSelection == nil || got.LastWorkingSelection.Selected != "node" || len(got.Samples["node"]) != 1 {
		t.Fatal("restart discarded remembered route or history")
	}
	if err := InvalidateRuntimeHealth(t.TempDir()); err != nil {
		t.Fatal("first installation failed:", err)
	}
}

func TestUnrestoredSheetDoesNotInterruptOtherStartupProofs(t *testing.T) {
	base := healthFixture(false).HealthPolicies["europe"]
	base.Candidates = []string{"node"}
	pool := healthPool{HealthPolicies: map[string]healthPolicyContract{"route": base}}
	item := newPolicyHealthState()
	item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "node", "", false
	runtime := &fakeSelectorRuntime{pool: pool, current: map[string]string{"route": "block"}}
	controller := &healthController{opts: Options{StateRoot: t.TempDir()}, runtime: runtime, state: healthState{"route": item}}
	if err := controller.checkDuringProbe(time.Unix(2000, 0), pool); err != nil {
		t.Fatal("unrestored sheet interrupted startup:", err)
	}
	if len(runtime.availabilityCalls) != 0 {
		t.Fatal("old core's active route was probed as current")
	}
}
