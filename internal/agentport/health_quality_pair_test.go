package agentport

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestQualityPairUsesActualCompletionTime(t *testing.T) {
	now := time.Unix(1000, 0)
	p := policySettings(healthPolicy{}, "best")
	for _, gap := range []time.Duration{10 * time.Second, 10*time.Second + time.Millisecond} {
		item := newPolicyHealthState()
		a, b := successfulEvidence(600), successfulEvidence(400)
		a.ObservedAt, b.ObservedAt = now, now.Add(gap)
		comparison := regularOptimizationComparison(now.Add(gap), "a", "b", map[string]probeEvidence{"a": a, "b": b}, item, true, true, p)
		want := optimizationWin
		if gap > qualityPairWindow {
			want = optimizationInconclusive
		}
		if comparison.Result != want || comparison.ActiveAt != 1000 || comparison.CandidateAt != qualityTimestamp(now, b) {
			t.Fatalf("gap=%s: %+v", gap, comparison)
		}
	}
}

func TestQualityMedianExpiresAndDoesNotSmoothPriorityOrFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	p := policySettings(healthPolicy{}, "best")
	a, b, c := 100, 800, 120
	samples := []healthSample{{At: 995, OK: true, DelayMS: &a}, {At: 997, OK: true, DelayMS: &b}, {At: 1000, OK: true, DelayMS: &c}}
	if value := recentQualityMedian(now, samples, p); value == nil || *value != 120 {
		t.Fatal("short median not used")
	}
	p.mode = "priority"
	if value := recentQualityMedian(now, samples[:2], p); value == nil || *value != 800 {
		t.Fatal("priority return was smoothed")
	}
	p.mode = "best"
	if value := recentQualityMedian(now.Add(121*time.Second), samples, p); value != nil {
		t.Fatal("old samples survived expiry")
	}
	samples[2].OK = false
	if value := recentQualityMedian(now, samples, p); value != nil {
		t.Fatal("failure hidden by older success")
	}
}

func TestLatestPairAndMedianMustBothShowImprovement(t *testing.T) {
	now := time.Unix(1000, 0)
	p := policySettings(healthPolicy{}, "best")
	p.improvement = 50
	for _, scenario := range []string{"latest-only", "median-only", "both"} {
		item := newPolicyHealthState()
		old, latest := 650, 400
		if scenario == "median-only" {
			old, latest = 400, 590
		}
		if scenario == "both" {
			old, latest = 400, 400
		}
		active := 600
		item.Samples["a"] = []healthSample{{At: 999, OK: true, DelayMS: &active}}
		item.Samples["b"] = []healthSample{{At: 997, OK: true, DelayMS: &old}, {At: 998, OK: true, DelayMS: &old}, {At: 1000, OK: true, DelayMS: &latest}}
		pair := regularOptimizationComparison(now, "a", "b", map[string]probeEvidence{"a": successfulEvidence(active), "b": successfulEvidence(latest)}, item, true, true, p)
		if (pair.Result == optimizationWin) != (scenario == "both") {
			t.Fatalf("%s: %+v", scenario, pair)
		}
	}
}

func TestOneSharedSlotFinishesPairsForThreeAndSixListsAtLongIntervals(t *testing.T) {
	for _, count := range []int{3, 6} {
		for _, interval := range []int{3, 30, 60} {
			t.Run(fmt.Sprintf("%d-lists/%d-seconds", count, interval), func(t *testing.T) {
				base := healthFixture(false).HealthPolicies["europe"]
				pool := healthPool{Version: 4, ProbeBudget: 1, HealthPolicies: map[string]healthPolicyContract{}}
				runtime := &fakeSelectorRuntime{current: map[string]string{}, probes: map[string]probeEvidence{}}
				controller := &healthController{opts: Options{StateRoot: t.TempDir(), HealthInterval: 30 * time.Second}, runtime: runtime, stateLoaded: true, state: healthState{}, warmStarted: map[string]bool{}}
				for _, id := range []string{"a", "b", "c", "d", "e", "f"}[:count] {
					active, reserve := id+"-active", id+"-reserve"
					contract := base
					contract.Mode, contract.Groups = "best", nil
					contract.Candidates = []string{active, reserve}
					contract.Nodes = budgetContract(active, reserve).Nodes
					contract.Policy.ActiveCheckSeconds, contract.Policy.ActiveLivenessSeconds, contract.Policy.ProbeBatchSize = interval, 30, 1
					pool.HealthPolicies[id] = contract
					item := newPolicyHealthState()
					item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = active, active, true
					item.CandidateSignature = strings.Join(contract.Candidates, "\n")
					item.CandidateNodes = contract.Nodes
					item.AvailabilityOK, item.QualityOK = map[string]bool{}, map[string]bool{}
					for node, delay := range map[string]int{active: 600, reserve: 400} {
						value := delay
						item.LastProbeAt[node] = 800
						item.Samples[node] = []healthSample{{At: 800, OK: true, DelayMS: &value}}
						item.AvailabilityOK[node], item.QualityOK[node], item.Recoveries[node] = true, true, 3
						runtime.probes[node] = successfulEvidence(delay)
					}
					runtime.current[id], controller.state[id], controller.warmStarted[id] = active, item, true
				}
				runtime.pool = pool
				now := time.Unix(1000, 0)
				for tick := 0; tick < 240 && now.Before(time.Unix(1300, 0)); tick++ {
					runtime.probeCalls = nil
					if err := controller.Tick(now); err != nil {
						t.Fatal(err)
					}
					if len(runtime.probeCalls) > 1 {
						t.Fatalf("shared slot exceeded: %v", runtime.probeCalls)
					}
					done := true
					for id, item := range controller.state {
						done = done && item.Selected == id+"-reserve"
					}
					if done {
						// One shared slot must also rotate all lists; allow its bounded
						// per-list work, never an additional quality interval.
						if now.Sub(time.Unix(1000, 0)) > time.Duration(max(interval, 30)+4*count)*time.Second {
							t.Fatal("paired scheduling added a quality cycle")
						}
						return
					}
					delay := controller.nextIntervalAt(now)
					if delay <= 0 {
						delay = time.Second
					}
					now = now.Add(delay)
				}
				for id, item := range controller.state {
					if item.Selected != id+"-reserve" {
						t.Errorf("%s starved: selected=%s checks=%d pair=%+v", id, item.Selected, item.OptimizationChecks, item.qualityPair)
					}
				}
			})
		}
	}
}

func TestExpiredPairContinuationDoesNotLoop(t *testing.T) {
	item := newPolicyHealthState()
	item.Selected = "a"
	item.Mode, item.RuntimeConfirmed = "best", true
	item.qualityPair = &qualityPairContinuation{Active: "a", Node: "b", Candidate: "b", StartedAt: time.Unix(1000, 0)}
	controller := &healthController{opts: Options{HealthInterval: 30 * time.Second}, state: healthState{"list": item}}
	if controller.nextIntervalAt(time.Unix(1001, 0)) != time.Second {
		t.Fatal("pair waits for quality interval")
	}
	if controller.nextIntervalAt(time.Unix(1011, 0)) == time.Second {
		t.Fatal("expired pair kept immediate wake")
	}
	planQualityContinuation(time.Unix(1011, 0), "a", nil, nil, nil, item, true)
	if item.qualityPair != nil {
		t.Fatal("expired continuation was rearmed")
	}
}

func TestDisplayDeltaRetainsOnlyMatchingContext(t *testing.T) {
	fixture := func() *policyHealthState {
		item := newPolicyHealthState()
		item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "a", "a", true
		item.CandidateLabels = map[string]string{"a": "Active", "b": "Reserve"}
		item.CandidateNodes = map[string]healthNode{"a": {Fingerprint: "one"}, "b": {Fingerprint: "two"}}
		item.AvailabilityOK = map[string]bool{"a": true, "b": true}
		a, b := 600, 400
		item.Samples["a"] = []healthSample{{At: 1000, OK: true, DelayMS: &a}}
		item.Samples["b"] = []healthSample{{At: 1001, OK: true, DelayMS: &b}}
		refreshDisplayComparisons(time.Unix(1002, 0), item)
		return item
	}
	item := fixture()
	if len(item.displayComparisons) != 2 {
		t.Fatal("valid pair not saved")
	}
	a := 650
	item.Samples["a"] = []healthSample{{At: 1100, OK: true, DelayMS: &a}}
	refreshDisplayComparisons(time.Unix(1100, 0), item)
	if saved := item.displayComparisons["b"].Pair; saved.ActiveAt != 1000 || saved.CandidateAt != 1001 || saved.ActiveDelayMS != 600 {
		t.Fatal("unpaired update overwrote display comparison")
	}
	for name, change := range map[string]func(*policyHealthState){
		"active":               func(s *policyHealthState) { s.RuntimeSelected, s.Selected = "b", "b" },
		"active-parameters":    func(s *policyHealthState) { s.CandidateNodes["a"] = healthNode{Fingerprint: "new"} },
		"candidate-parameters": func(s *policyHealthState) { s.CandidateNodes["b"] = healthNode{Fingerprint: "new"} },
		"target":               func(s *policyHealthState) { s.LatencyMeasurement = "new-target" },
		"removed":              func(s *policyHealthState) { delete(s.CandidateLabels, "b") },
	} {
		t.Run(name, func(t *testing.T) {
			s := fixture()
			change(s)
			refreshDisplayComparisons(time.Unix(2000, 0), s)
			if _, ok := s.displayComparisons["b"]; ok {
				t.Fatal("comparison survived context change")
			}
		})
	}
}

func TestLargeSweepRefreshesActiveWithinBatchesAndKeepsAllDeltas(t *testing.T) {
	controller, item, runtime := stagedOptimizationController(t)
	contract := runtime.pool.HealthPolicies["europe"]
	contract.Policy.ProbeBatchSize = 3
	contract.Candidates = []string{"active"}
	contract.Nodes = budgetContract("active").Nodes
	active := 300
	item.Samples["active"] = []healthSample{{At: 800, OK: true, DelayMS: &active}}
	item.LastProbeAt["active"] = 800
	runtime.probes["active"] = successfulEvidence(active)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("node-%02d", i)
		contract.Candidates = append(contract.Candidates, id)
		contract.Nodes[id] = healthNode{Label: id, Fingerprint: id}
		delay := 500 + i
		item.Samples[id] = []healthSample{{At: 800, OK: true, DelayMS: &delay}}
		item.LastProbeAt[id], item.Recoveries[id] = 800, 3
		item.AvailabilityOK[id], item.QualityOK[id] = true, true
		runtime.probes[id] = successfulEvidence(delay)
	}
	item.CandidateNodes = contract.Nodes
	item.CandidateSignature = strings.Join(contract.Candidates, "\n")
	runtime.pool.HealthPolicies["europe"] = contract
	runtime.pool.ProbeBudget = 3
	for second := int64(1000); second < 1090; second += 3 {
		runtime.probeCalls = nil
		if err := controller.Tick(time.Unix(second, 0)); err != nil {
			t.Fatal(err)
		}
		if len(runtime.probeCalls) > 3 {
			t.Fatal("active refresh exceeded the shared batch")
		}
		monitorSnapshot(controller.state, runtimeSnapshot(controller.state, time.Unix(second, 0)))
	}
	for _, node := range contract.Candidates {
		if _, ok := item.displayComparisons[node]; !ok {
			t.Errorf("%s never received a paired Delta", node)
		}
	}
	if item.Selected != "active" {
		t.Fatal("display refresh changed the selected path")
	}
}

func TestNewActiveAloneCannotConfirmCachedCandidateAgain(t *testing.T) {
	item := newPolicyHealthState()
	p := policySettings(healthPolicy{}, "best")
	now := time.Unix(1000, 0)
	first := regularOptimizationComparison(now, "a", "b", map[string]probeEvidence{"a": successfulEvidence(600), "b": successfulEvidence(400)}, item, true, true, p)
	gatePlannedOptimization(now, "a", "b", "meaningfully-faster", first, item, p)
	b := 400
	item.Samples["b"] = []healthSample{{At: 1000, OK: true, DelayMS: &b}}
	second := regularOptimizationComparison(now.Add(time.Second), "a", "b", map[string]probeEvidence{"a": successfulEvidence(600)}, item, true, true, p)
	desired, _ := gatePlannedOptimization(now.Add(time.Second), "a", "b", "meaningfully-faster", second, item, p)
	if desired != "a" || item.OptimizationChecks != 1 {
		t.Fatal("reused candidate supplied another confirmation")
	}
}

type serialQualityTestRuntime struct {
	*fakeSelectorRuntime
	now *time.Time
}

func (*serialQualityTestRuntime) qualityProbeParallelism() int { return 1 }

func (runtime *serialQualityTestRuntime) Probe(node string) probeEvidence {
	evidence := runtime.fakeSelectorRuntime.Probe(node)
	*runtime.now = runtime.now.Add(4 * time.Second)
	evidence.ObservedAt = *runtime.now
	return evidence
}

func TestSlowSingleLaneSweepStillProducesComparableDeltas(t *testing.T) {
	controller, item, primary := stagedOptimizationController(t)
	contract := primary.pool.HealthPolicies["europe"]
	contract.Policy.ProbeBatchSize = 10
	contract.Policy.ActiveLivenessSeconds = 30
	contract.Candidates = []string{"active"}
	contract.Nodes = budgetContract("active").Nodes
	active := 300
	item.Samples["active"] = []healthSample{{At: 800, OK: true, DelayMS: &active}}
	item.LastProbeAt["active"] = 800
	primary.probes["active"] = successfulEvidence(active)
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("slow-%d", i)
		contract.Candidates = append(contract.Candidates, id)
		contract.Nodes[id] = healthNode{Label: id, Fingerprint: id}
		delay := 500 + i
		item.Samples[id] = []healthSample{{At: 800, OK: true, DelayMS: &delay}}
		item.LastProbeAt[id], item.Recoveries[id] = 800, 3
		item.AvailabilityOK[id], item.QualityOK[id] = true, true
		primary.probes[id] = successfulEvidence(delay)
	}
	item.CandidateNodes = contract.Nodes
	item.CandidateSignature = strings.Join(contract.Candidates, "\n")
	primary.pool.HealthPolicies["europe"] = contract
	primary.pool.ProbeBudget = 10
	now := time.Unix(1000, 0)
	controller.runtime = &serialQualityTestRuntime{primary, &now}
	for tick := 0; tick < 60 && now.Before(time.Unix(1120, 0)); tick++ {
		primary.probeCalls = nil
		started := now
		if err := controller.Tick(started); err != nil {
			t.Fatal(err)
		}
		if len(primary.probeCalls) > 1 {
			t.Fatal("one lane accumulated a long sequential batch")
		}
		monitorSnapshot(controller.state, runtimeSnapshot(controller.state, now))
		now = now.Add(max(time.Second, controller.nextIntervalAt(now)))
	}
	for _, node := range contract.Candidates {
		if _, ok := item.displayComparisons[node]; !ok {
			t.Errorf("slow lane left %s without comparable Delta", node)
		}
	}
	if item.Selected != "active" {
		t.Fatal("latency display changed the live route")
	}
}
