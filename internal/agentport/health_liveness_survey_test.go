package agentport

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A short job finishes before the responsive worker's one-second callback.
// Advance the same clock by both job duration and the actual scheduled sleep.
type surveyProofRuntime struct {
	*fakeSelectorRuntime
	clock       time.Time
	proofs      []time.Time
	maxProofGap time.Duration
}

func (runtime *surveyProofRuntime) completed(node string, evidence probeEvidence) probeEvidence {
	evidence.QualityStartedAt = runtime.clock
	runtime.clock = runtime.clock.Add(425 * time.Millisecond)
	evidence.ObservedAt = runtime.clock
	if node == "active" && evidence.OK {
		if count := len(runtime.proofs); count > 0 {
			runtime.maxProofGap = max(runtime.maxProofGap, runtime.clock.Sub(runtime.proofs[count-1]))
		}
		runtime.proofs = append(runtime.proofs, runtime.clock)
	}
	return evidence
}

func (runtime *surveyProofRuntime) Probe(node string) probeEvidence {
	return runtime.completed(node, runtime.fakeSelectorRuntime.Probe(node))
}

func (runtime *surveyProofRuntime) ProbeAvailability(node string) probeEvidence {
	return runtime.completed(node, runtime.fakeSelectorRuntime.ProbeAvailability(node))
}

func surveyProofFixture(t *testing.T, mode string) (*healthController, *policyHealthState, *surveyProofRuntime) {
	t.Helper()
	contract := healthFixture(false).HealthPolicies["europe"]
	contract.Mode, contract.Groups = mode, nil
	contract.Candidates = []string{"active"}
	for index := 1; index < 24; index++ {
		contract.Candidates = append(contract.Candidates, fmt.Sprintf("reserve-%02d", index))
	}
	contract.Nodes = budgetContract(contract.Candidates...).Nodes
	contract.Policy.ActiveCheckSeconds = 60
	contract.Policy.ActiveLivenessSeconds = 3
	contract.Policy.ProbeBatchSize = 1
	pool := healthPool{Version: 4, ProbeBudget: 1, ProbeLanes: 1, HealthPolicies: map[string]healthPolicyContract{"europe": contract}}
	base := &fakeSelectorRuntime{pool: pool, current: map[string]string{"europe": "active"}, probes: map[string]probeEvidence{}, underlay: underlayEvidence{Known: true, WANOK: true, DNSOK: true}}
	item := newPolicyHealthState()
	ensureHealthMaps(item)
	item.Mode = mode
	item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "active", "active", true
	item.CandidateSignature, item.CandidateNodes = strings.Join(contract.Candidates, "\n"), contract.Nodes
	item.ScanQueue = append([]string(nil), contract.Candidates...)
	item.AvailabilityOK, item.QualityOK = map[string]bool{}, map[string]bool{}
	for _, node := range contract.Candidates {
		delay := 500
		base.probes[node] = successfulEvidence(delay)
		item.AvailabilityOK[node], item.QualityOK[node], item.Recoveries[node] = true, true, 3
		item.LastProbeAt[node], item.LastGoodAt[node] = 1000, 1000
		item.Samples[node] = []healthSample{{At: 1000, OK: true, DelayMS: &delay}}
	}
	runtime := &surveyProofRuntime{fakeSelectorRuntime: base, clock: time.Unix(1060, 0)}
	controller := &healthController{opts: Options{StateRoot: t.TempDir(), HealthInterval: time.Minute}, runtime: runtime,
		stateLoaded: true, state: healthState{"europe": item}, warmStarted: map[string]bool{"europe": true}}
	return controller, item, runtime
}

func TestSingleSlotShortSurveyKeepsActiveProofsAndQualityProgress(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		t.Run(mode, func(t *testing.T) {
			controller, _, runtime := surveyProofFixture(t, mode)
			deadline := runtime.clock.Add(40 * time.Second)
			for cycle := 0; cycle < 80 && runtime.clock.Before(deadline); cycle++ {
				before := len(runtime.probeCalls)
				if err := controller.Tick(runtime.clock); err != nil {
					t.Fatal(err)
				}
				if len(runtime.probeCalls)-before > 1 {
					t.Fatal("one-slot quality budget exceeded")
				}
				if len(runtime.proofs) == 0 || runtime.maxProofGap > 6*time.Second || runtime.clock.Sub(runtime.proofs[len(runtime.proofs)-1]) > 6*time.Second {
					t.Fatalf("short survey starved active liveness: max gap=%s quality=%v availability=%v", runtime.maxProofGap, runtime.probeCalls, runtime.availabilityCalls)
				}
				runtime.clock = runtime.clock.Add(controller.nextIntervalAt(runtime.clock))
			}
			seen := map[string]bool{}
			for _, node := range runtime.probeCalls {
				seen[node] = true
			}
			if len(seen) < 6 || len(runtime.selections) != 0 {
				t.Fatal("liveness displaced inventory progress or changed a healthy selection")
			}
			t.Logf("active proofs=%d max gap=%s quality nodes=%d", len(runtime.proofs), runtime.maxProofGap, len(seen))
		})
	}
}

func TestSingleSlotShortSurveyDetectsOutageWithoutTrafficSignals(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		t.Run(mode, func(t *testing.T) {
			controller, item, runtime := surveyProofFixture(t, mode)
			start := runtime.clock
			var failedAt time.Time
			for cycle := 0; cycle < 100 && runtime.clock.Sub(start) < time.Minute; cycle++ {
				if failedAt.IsZero() && runtime.clock.Sub(start) >= 15*time.Second {
					failedAt = runtime.clock
					runtime.probes["active"] = probeEvidence{Failure: probeFailureTimeout}
				}
				if err := controller.Tick(runtime.clock); err != nil {
					t.Fatal(err)
				}
				if item.Selected != "active" {
					if failedAt.IsZero() || runtime.clock.Sub(failedAt) > 15*time.Second || item.LastSwitchReason != "active-unavailable" {
						t.Fatalf("outage was delayed or a healthy node changed: selected=%s reason=%s", item.Selected, item.LastSwitchReason)
					}
					t.Logf("active outage recovered after %s", runtime.clock.Sub(failedAt))
					return
				}
				runtime.clock = runtime.clock.Add(controller.nextIntervalAt(runtime.clock))
			}
			t.Fatal("active outage waited for the inventory sweep")
		})
	}
}
