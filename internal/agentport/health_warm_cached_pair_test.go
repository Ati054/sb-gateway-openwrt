package agentport

import (
	"testing"
	"time"
)

func TestWarmCachedHalfRetainsWaveCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name          string
		candidateWave uint64
		wantPair      bool
	}{
		{"serial-cannot-use-parallel-active", 0, false},
		{"different-parallel-wave", 18, false},
		{"same-overlapping-wave", 17, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, r, base := warmControllerFixture(t, 2, 1, 1)
			contract, item := r.pool.HealthPolicies["route"], c.state["route"]
			c.prepareWarmPlanning(contract, item)
			a, b := successfulEvidence(200), successfulEvidence(400)
			activeDelay, candidateDelay := 900, 100
			a.Warm = &warmQualityEvidence{Measurement: qualityMeasurementWarm, Wave: 17, DelayMS: &activeDelay,
				StartedAt: base, ObservedAt: base.Add(900 * time.Millisecond)}
			b.Warm = &warmQualityEvidence{Measurement: qualityMeasurementWarm, Wave: tc.candidateWave, DelayMS: &candidateDelay,
				StartedAt: base.Add(200 * time.Millisecond), ObservedAt: base.Add(300 * time.Millisecond)}
			now := base.Add(time.Second)
			p := policySettings(contract.Policy, "best")
			collectWarmPlanning(now, contract.Candidates, map[string]probeEvidence{"node-00": a, "node-01": b}, item, p)
			item.OptimizationBaseline, item.OptimizationCandidate = "node-00", "node-01"
			got := planningComparison(now, "node-00", "node-01", map[string]probeEvidence{"node-01": b}, item, true, true, p)
			if (got != nil) != tc.wantPair {
				t.Fatalf("cached active wave compatibility lost: got=%+v wantPair=%v", got, tc.wantPair)
			}
			if got != nil && got.Result != optimizationWin {
				t.Fatalf("matching warm pair did not retain its advantage: %+v", got)
			}
		})
	}
}

func TestWarmNominatedCandidateCanFinishOnActiveTick(t *testing.T) {
	c, r, base := warmControllerFixture(t, 2, 1, 1)
	if err := c.Tick(base); err != nil {
		t.Fatal(err)
	}
	item, contract := c.state["route"], r.pool.HealthPolicies["route"]
	// An eligible candidate half is already delivered. Only the active side
	// is due now; nomination has not yet earned its first confirmation.
	r.at = base.Add(time.Second)
	b := r.ProbeWithQualityRequest("node-01", qualityProbeRequest{Measurement: qualityMeasurementWarm})
	p := policySettings(contract.Policy, "best")
	collectWarmPlanning(base.Add(2*time.Second), contract.Candidates, map[string]probeEvidence{"node-01": b}, item, p)
	delete(item.WarmPlanningSamples, "node-00")
	delete(item.warmPlanningLatest, "node-00")
	item.WarmPlanningLastProbeAt["node-00"] = float64(base.Add(-65 * time.Second).Unix())
	item.OptimizationBaseline, item.OptimizationCandidate, item.OptimizationChecks = "node-00", "node-01", 0
	item.OptimizationLastResult = &optimizationComparison{At: base.Add(time.Second).UTC().Format(time.RFC3339Nano),
		Candidate: "node-01", CandidateAt: qualityTimestamp(base, probeEvidence{ObservedAt: b.Warm.ObservedAt}), Result: optimizationInconclusive, Reason: "pair-incomplete"}
	item.qualityPair, item.ScanQueue = nil, []string{"node-00"}
	c.regularNext["route"] = base.Add(2 * time.Second)
	before := len(r.probeCalls)
	r.at = base.Add(2 * time.Second)
	if err := c.Tick(r.at); err != nil {
		t.Fatal(err)
	}
	if len(r.probeCalls)-before != 1 || r.probeCalls[before] != "node-00" {
		t.Fatalf("active completion spent another candidate job: %v", r.probeCalls[before:])
	}
	if item.OptimizationChecks != 1 || item.Selected != "node-00" {
		t.Fatalf("active completion lost first proof or switched early: candidate=%s checks=%d selected=%s", item.OptimizationCandidate, item.OptimizationChecks, item.Selected)
	}
	r.at = item.qualityFinishedAt
	if err := c.Tick(r.at); err != nil {
		t.Fatal(err)
	}
	if item.OptimizationChecks != 1 || item.Selected != "node-00" {
		t.Fatal("reusing the completed pair earned another confirmation")
	}
}

func TestWarmCachedHalfRequiresExactQualifiedObservation(t *testing.T) {
	for _, scenario := range []string{"valid", "missing-warm", "missing-history", "failed", "unknown-start", "newer-processed", "missing-processed", "failed-history", "future"} {
		t.Run(scenario, func(t *testing.T) {
			c, r, base := warmControllerFixture(t, 2, 1, 1)
			contract, item := r.pool.HealthPolicies["route"], c.state["route"]
			c.prepareWarmPlanning(contract, item)
			delay := 900
			observed := base.Add(time.Second)
			warm := warmQualityEvidence{Measurement: qualityMeasurementWarm, Wave: 29, StartedAt: base, ObservedAt: observed, DelayMS: &delay}
			item.warmPlanningLatest["node-00"] = warm
			item.warmPlanningCountedAt["node-00"] = observed
			item.WarmPlanningSamples["node-00"] = []healthSample{{At: qualityTimestamp(base, probeEvidence{ObservedAt: observed}), OK: true, DelayMS: &delay}}
			switch scenario {
			case "missing-warm":
				delete(item.warmPlanningLatest, "node-00")
			case "missing-history":
				delete(item.WarmPlanningSamples, "node-00")
			case "failed":
				warm.Failed = true
				item.warmPlanningLatest["node-00"] = warm
			case "unknown-start":
				warm.StartedAt = time.Time{}
				item.warmPlanningLatest["node-00"] = warm
			case "newer-processed":
				item.warmPlanningCountedAt["node-00"] = observed.Add(time.Millisecond)
			case "missing-processed":
				delete(item.warmPlanningCountedAt, "node-00")
			case "failed-history":
				item.WarmPlanningSamples["node-00"][0].OK = false
			case "future":
				warm.ObservedAt = base.Add(3 * time.Second)
				item.warmPlanningLatest["node-00"] = warm
				item.WarmPlanningSamples["node-00"][0].At = qualityTimestamp(base, probeEvidence{ObservedAt: warm.ObservedAt})
			}
			got, valid := cachedWarmPairEvidence(base.Add(2*time.Second), "node-00", warmPlanningView(item))
			if valid != (scenario == "valid") {
				t.Fatalf("unexpected cached qualification: %s valid=%v", scenario, valid)
			}
			if valid && (got.QualityWave != 29 || !got.QualityStartedAt.Equal(base) || !got.ObservedAt.Equal(observed)) {
				t.Fatalf("cached evidence metadata changed: %+v", got)
			}
		})
	}
}
