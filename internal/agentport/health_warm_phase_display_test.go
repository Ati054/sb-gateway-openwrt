package agentport

import (
	"testing"
	"time"
)

func phaseDisplayFixture(t *testing.T) (*healthController, *warmPlanningTestRuntime, *policyHealthState, time.Time) {
	t.Helper()
	c, r, base := warmControllerFixture(t, 2, 1, 1)
	item, contract := c.state["route"], r.pool.HealthPolicies["route"]
	c.prepareWarmPlanning(contract, item)
	item.CandidateLabels = map[string]string{"node-00": "Active", "node-01": "Reserve"}
	return c, r, item, base
}

func warmPhasePair(base time.Time, waveA, waveB uint64, startA, endA, startB, endB time.Duration) map[string]probeEvidence {
	a, b := successfulEvidence(200), successfulEvidence(400)
	ad, bd := 900, 100
	a.Warm = &warmQualityEvidence{Measurement: qualityMeasurementWarm, Wave: waveA, StartedAt: base.Add(startA), ObservedAt: base.Add(endA), DelayMS: &ad}
	b.Warm = &warmQualityEvidence{Measurement: qualityMeasurementWarm, Wave: waveB, StartedAt: base.Add(startB), ObservedAt: base.Add(endB), DelayMS: &bd}
	return map[string]probeEvidence{"node-00": a, "node-01": b}
}

func TestWarmCachedParallelCandidateAllowsReverseCompletion(t *testing.T) {
	_, r, item, base := phaseDisplayFixture(t)
	contract := r.pool.HealthPolicies["route"]
	now := base.Add(time.Second)
	measured := warmPhasePair(base, 17, 17, 0, 300*time.Millisecond, 100*time.Millisecond, 900*time.Millisecond)
	p := policySettings(contract.Policy, "best")
	collectWarmPlanning(now, contract.Candidates, measured, item, p)
	item.OptimizationBaseline, item.OptimizationCandidate = "node-00", "node-01"
	got := planningComparison(now, "node-00", "node-01", map[string]probeEvidence{"node-00": measured["node-00"]}, item, true, true, p)
	if got == nil || got.Result != optimizationWin {
		t.Fatalf("overlapping parallel pair was rejected by completion order: %+v", got)
	}
}

func TestWarmRetainedDeltaRequiresCompatibleLatestPhases(t *testing.T) {
	for _, scenario := range []string{"mixed", "different", "nonoverlap", "missing-latest", "future", "reverse-overlap", "serial"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, item, base := phaseDisplayFixture(t)
			contract := r.pool.HealthPolicies["route"]
			p := policySettings(contract.Policy, "best")
			first := warmPhasePair(base, 7, 7, 0, 900*time.Millisecond, 200*time.Millisecond, 300*time.Millisecond)
			collectWarmPlanning(base.Add(time.Second), contract.Candidates, first, item, p)
			refreshDisplayComparisons(base.Add(time.Second), item)
			before, ok := item.displayComparisons["node-01"]
			if !ok {
				t.Fatal("initial qualified Delta missing")
			}
			waveA, waveB := uint64(8), uint64(8)
			startA, endA, startB, endB := 2*time.Second, 2900*time.Millisecond, 2200*time.Millisecond, 2300*time.Millisecond
			valid := false
			switch scenario {
			case "mixed":
				waveB = 0
			case "different":
				waveB = 9
			case "future":
				endB = 5 * time.Second
			case "nonoverlap":
				startB, endB = 3100*time.Millisecond, 3200*time.Millisecond
			case "reverse-overlap":
				endA, endB = 2300*time.Millisecond, 2900*time.Millisecond
				valid = true
			case "serial":
				waveA, waveB = 0, 0
				startB, endB = 3*time.Second, 3100*time.Millisecond
				valid = true
			}
			now := base.Add(4 * time.Second)
			second := warmPhasePair(base, waveA, waveB, startA, endA, startB, endB)
			collectWarmPlanning(now, contract.Candidates, second, item, p)
			if scenario == "missing-latest" {
				delete(item.warmPlanningLatest, "node-01")
			}
			refreshDisplayComparisons(now, item)
			after := item.displayComparisons["node-01"]
			if !valid && after != before {
				t.Fatalf("unqualified phases overwrote retained Delta: before=%+v after=%+v", before, after)
			}
			if valid && (after.Pair.ActiveAt == before.Pair.ActiveAt || after.Pair.CandidateAt == before.Pair.CandidateAt) {
				t.Fatal("qualified pair did not update Delta")
			}
		})
	}
}
