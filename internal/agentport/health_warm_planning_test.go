package agentport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type warmPlanningTestRuntime struct {
	*fakeSelectorRuntime
	at             time.Time
	lanes          int
	warm           map[string]int
	warmFailures   map[string]bool
	measurements   []qualityMeasurement
	heads          int
	wave           uint64
	blocked        map[string]bool
	batches        [][]string
	attempts       map[string][]time.Time
	advanceClock   bool
	availabilityAt map[string][]time.Time
}

func (runtime *warmPlanningTestRuntime) ProbeAvailability(node string) probeEvidence {
	e := runtime.fakeSelectorRuntime.ProbeAvailability(node)
	e.ObservedAt = runtime.at.Add(100 * time.Millisecond)
	if runtime.availabilityAt == nil {
		runtime.availabilityAt = map[string][]time.Time{}
	}
	runtime.availabilityAt[node] = append(runtime.availabilityAt[node], e.ObservedAt)
	if runtime.advanceClock {
		runtime.at = e.ObservedAt
	}
	return e
}

func (runtime *warmPlanningTestRuntime) qualityProbeParallelism() int { return runtime.lanes }

func (runtime *warmPlanningTestRuntime) Probe(node string) probeEvidence {
	return runtime.ProbeWithQualityRequest(node, qualityProbeRequest{Measurement: qualityMeasurementCold})
}

func (runtime *warmPlanningTestRuntime) ProbeWithQualityRequest(node string, request qualityProbeRequest) probeEvidence {
	runtime.probeCalls = append(runtime.probeCalls, node)
	runtime.measurements = append(runtime.measurements, request.Measurement)
	runtime.heads++
	if runtime.attempts != nil {
		runtime.attempts[node] = append(runtime.attempts[node], runtime.at)
	}
	evidence := runtime.probes[node]
	evidence.QualityStartedAt, evidence.ObservedAt = runtime.at, runtime.at.Add(500*time.Millisecond)
	if runtime.blocked[node] {
		// A timed-out primary HEAD followed by one successful availability GET.
		runtime.heads++
		evidence.PrimaryQualityFailed, evidence.QualityUnmeasured, evidence.DelayMS = false, true, nil
		evidence.ObservedAt = runtime.at.Add(5 * time.Second)
		if request.Measurement == qualityMeasurementWarm {
			evidence.Warm = &warmQualityEvidence{Measurement: qualityMeasurementWarm, ObservedAt: evidence.ObservedAt, Failed: true}
		}
		return runtime.finishModelObservation(evidence)
	}
	if request.Measurement == qualityMeasurementWarm {
		runtime.heads++
		delay := runtime.warm[node]
		warm := &warmQualityEvidence{Measurement: qualityMeasurementWarm, StartedAt: runtime.at.Add(500 * time.Millisecond),
			ObservedAt: runtime.at.Add(time.Duration(500+delay) * time.Millisecond), DelayMS: &delay, Failed: runtime.warmFailures[node]}
		if warm.Failed {
			warm.DelayMS = nil
		}
		evidence.Warm = warm
	}
	if request.Measurement == qualityMeasurementFreshPaired {
		evidence.Planning = &planningQualityEvidence{Measurement: qualityMeasurementFreshPaired,
			StartedAt: evidence.QualityStartedAt, ObservedAt: evidence.ObservedAt, DelayMS: evidence.DelayMS,
			Failed: !evidence.OK || evidence.PrimaryQualityFailed || evidence.QualityUnmeasured}
	}
	return runtime.finishModelObservation(evidence)
}

func (runtime *warmPlanningTestRuntime) finishModelObservation(evidence probeEvidence) probeEvidence {
	if runtime.advanceClock {
		if evidence.ObservedAt.After(runtime.at) {
			runtime.at = evidence.ObservedAt
		}
		if evidence.Warm != nil && evidence.Warm.ObservedAt.After(runtime.at) {
			runtime.at = evidence.Warm.ObservedAt
		}
		if evidence.Planning != nil && evidence.Planning.ObservedAt.After(runtime.at) {
			runtime.at = evidence.Planning.ObservedAt
		}
	}
	return evidence
}

func (runtime *warmPlanningTestRuntime) ProbeQualityParallel(nodes []string) map[string]probeEvidence {
	return runtime.ProbeQualityParallelWithMeasurement(nodes, qualityMeasurementCold)
}

func (runtime *warmPlanningTestRuntime) ProbeQualityParallelWithMeasurement(nodes []string, metric qualityMeasurement) map[string]probeEvidence {
	result := make(map[string]probeEvidence)
	runtime.batches = append(runtime.batches, append([]string(nil), nodes...))
	started := runtime.at
	runtime.wave += 2
	for _, node := range nodes {
		if runtime.advanceClock && runtime.lanes > 1 {
			runtime.at = started
		}
		evidence := runtime.ProbeWithQualityRequest(node, qualityProbeRequest{Measurement: metric})
		if runtime.lanes > 1 {
			evidence.QualityWave = runtime.wave
			if evidence.Planning != nil {
				evidence.Planning.Wave = runtime.wave
			}
			if evidence.Warm != nil {
				evidence.Warm.Wave = runtime.wave + 1
			}
		}
		result[node] = evidence
	}
	failedPeer := false
	finished := started
	for node, evidence := range result {
		failedPeer = failedPeer || runtime.blocked[node]
		if evidence.ObservedAt.After(finished) {
			finished = evidence.ObservedAt
		}
	}
	if metric == qualityMeasurementWarm && failedPeer && runtime.lanes > 1 {
		for node, evidence := range result {
			if !runtime.blocked[node] && evidence.Warm != nil {
				runtime.heads-- // The peer abort prevents the second HTTP request.
				evidence.Warm = &warmQualityEvidence{Measurement: qualityMeasurementWarm, ObservedAt: finished, Deferred: true, Wave: runtime.wave + 1}
				result[node] = evidence
			}
		}
	}
	if runtime.advanceClock {
		runtime.at = started
		for _, evidence := range result {
			runtime.finishModelObservation(evidence)
		}
	}
	return result
}

func warmControllerFixture(t *testing.T, count, lanes, budget int) (*healthController, *warmPlanningTestRuntime, time.Time) {
	t.Helper()
	base := time.Unix(1000, 0)
	nodes, metadata := make([]string, count), make(map[string]healthNode)
	probes, warm := make(map[string]probeEvidence), make(map[string]int)
	for i := range nodes {
		node := fmt.Sprintf("node-%02d", i)
		nodes[i], metadata[node] = node, healthNode{Fingerprint: "test-endpoint-" + node, Protocol: "vless", Transport: "ws"}
		probes[node], warm[node] = successfulEvidence(200), 900
	}
	probes[nodes[1]], warm[nodes[1]] = successfulEvidence(400), 100
	contract := healthPolicyContract{Mode: "best", Candidates: nodes, Nodes: metadata,
		Policy: healthPolicy{LatencyMeasurement: "gstatic-head-v1", ActiveCheckSeconds: 60, ActiveLivenessSeconds: 3, ProbeBatchSize: budget, SwitchImprovementMS: 200}}
	pool := healthPool{Version: 4, ProbeBudget: budget, ProbeLanes: lanes, HealthPolicies: map[string]healthPolicyContract{"route": contract}}
	item := newPolicyHealthState()
	item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = nodes[0], nodes[0], true
	item.Mode, item.LatencyMeasurement, item.CandidateSignature = "best", "gstatic-head-v1", strings.Join(nodes, "\n")
	item.CandidateNodes, item.AvailabilityOK, item.QualityOK = metadata, map[string]bool{}, map[string]bool{}
	for _, node := range nodes {
		value := *probes[node].DelayMS
		item.Samples[node] = []healthSample{{At: 998, OK: true, DelayMS: &value}}
		item.LastProbeAt[node], item.Recoveries[node], item.AvailabilityOK[node], item.QualityOK[node] = 935, 3, true, true
	}
	runtime := &warmPlanningTestRuntime{fakeSelectorRuntime: &fakeSelectorRuntime{pool: pool, current: map[string]string{"route": nodes[0]}, probes: probes},
		at: base, lanes: lanes, warm: warm, warmFailures: map[string]bool{}, blocked: map[string]bool{}, attempts: map[string][]time.Time{}}
	controller := &healthController{opts: Options{StateRoot: t.TempDir(), URLTestWarmCompare: true}, runtime: runtime, stateLoaded: true,
		state: healthState{"route": item}, warmStarted: map[string]bool{"route": true}, processedPool: pool}
	return controller, runtime, base
}

func TestWarmPlanningSwitchesOnlyOnNewWarmPairs(t *testing.T) {
	for _, lanes := range []int{1, 2} {
		for _, count := range []int{2, 7, 24} {
			t.Run(fmt.Sprintf("lanes%d/nodes%d", lanes, count), func(t *testing.T) {
				controller, runtime, base := warmControllerFixture(t, count, lanes, lanes)
				item := controller.state["route"]
				var firstProof time.Time
				for second := 0; second <= 210; second++ {
					runtime.at = base.Add(time.Duration(second) * time.Second)
					before := len(runtime.probeCalls)
					if err := controller.Tick(runtime.at); err != nil {
						t.Fatal(err)
					}
					if len(runtime.probeCalls)-before > lanes {
						t.Fatal("quality job budget exceeded")
					}
					if item.OptimizationChecks == 1 && firstProof.IsZero() {
						firstProof = runtime.at
					}
					if item.Selected != "node-00" {
						if item.Selected != "node-01" || firstProof.IsZero() || runtime.at.Sub(firstProof) < 60*time.Second {
							t.Fatalf("invalid switch: selected=%s first=%s now=%s", item.Selected, firstProof, runtime.at)
						}
						if *item.MedianDelayMS["node-00"] != 200 || *item.MedianDelayMS["node-01"] != 400 {
							t.Fatal("warm metric overwrote cold history")
						}
						return
					}
				}
				t.Fatalf("warm improvement never converged: candidate=%s checks=%d calls=%v", item.OptimizationCandidate, item.OptimizationChecks, runtime.probeCalls)
			})
		}
	}
}

func TestWarmDefaultStartupAndPriorityUseCold(t *testing.T) {
	for _, scenario := range []string{"disabled", "startup", "priority"} {
		t.Run(scenario, func(t *testing.T) {
			controller, runtime, base := warmControllerFixture(t, 2, 2, 2)
			switch scenario {
			case "disabled":
				controller.opts.URLTestWarmCompare = false
			case "startup":
				controller.warmStarted["route"] = false
			case "priority":
				c := runtime.pool.HealthPolicies["route"]
				c.Mode = "priority"
				runtime.pool.HealthPolicies["route"] = c
			}
			if err := controller.Tick(base); err != nil {
				t.Fatal(err)
			}
			for _, m := range runtime.measurements {
				if m != qualityMeasurementCold {
					t.Fatalf("%s used warm", scenario)
				}
			}
		})
	}
}

func TestWarmFailureDoesNotMixMetricsOrEraseDisplay(t *testing.T) {
	controller, runtime, now := warmControllerFixture(t, 2, 2, 2)
	item, contract := controller.state["route"], runtime.pool.HealthPolicies["route"]
	controller.prepareWarmPlanning(contract, item)
	item.CandidateLabels = map[string]string{"node-00": "active", "node-01": "reserve"}
	measured := runtime.ProbeQualityParallelWithMeasurement(contract.Candidates, qualityMeasurementWarm)
	now = qualityEvaluationTime(now, measured)
	collectWarmPlanning(now, contract.Candidates, measured, item, policySettings(contract.Policy, "best"))
	refreshDisplayComparisons(now, item)
	saved, ok := item.displayComparisons["node-01"]
	if !ok || saved.Pair.Measurement != string(qualityMeasurementWarm) {
		t.Fatal("warm display missing")
	}
	item.OptimizationBaseline, item.OptimizationCandidate, item.OptimizationChecks = "node-00", "node-01", 1
	failure := successfulEvidence(100)
	failure.ObservedAt = now.Add(time.Second)
	failure.Warm = &warmQualityEvidence{Measurement: qualityMeasurementWarm, StartedAt: now, ObservedAt: failure.ObservedAt, Failed: true}
	collectWarmPlanning(failure.ObservedAt, contract.Candidates, map[string]probeEvidence{"node-01": failure}, item, policySettings(contract.Policy, "best"))
	refreshDisplayComparisons(failure.ObservedAt, item)
	if len(item.WarmPlanningSamples["node-01"]) != 0 || item.OptimizationChecks != 0 {
		t.Fatal("failed warm observation remained planning evidence")
	}
	if item.displayComparisons["node-01"] != saved {
		t.Fatal("failed warm observation erased last valid display")
	}
	if len(item.Samples["node-01"]) == 0 || !item.AvailabilityOK["node-01"] {
		t.Fatal("warm failure damaged cold evidence")
	}
}

func TestWarmPlanningContextAndRestoreDiscardSessionProof(t *testing.T) {
	controller, runtime, _ := warmControllerFixture(t, 2, 2, 2)
	item, contract := controller.state["route"], runtime.pool.HealthPolicies["route"]
	controller.prepareWarmPlanning(contract, item)
	item.OptimizationChecks = 1
	item.OptimizationCandidate = "node-01"
	contract.Nodes["node-01"] = healthNode{Fingerprint: "changed"}
	controller.prepareWarmPlanning(contract, item)
	if item.OptimizationChecks != 0 || len(item.WarmPlanningSamples) != 0 || item.Selected != "node-00" {
		t.Fatal("context did not reset session")
	}
	item.OptimizationBaseline, item.OptimizationCandidate, item.OptimizationChecks = "node-00", "node-01", 1
	body, err := json.Marshal(healthCheckpointState(healthState{"route": item}))
	if err != nil {
		t.Fatal(err)
	}
	var restored healthState
	if err := json.Unmarshal(body, &restored); err != nil {
		t.Fatal(err)
	}
	if restored["route"].OptimizationChecks != 0 || restored["route"].WarmPlanningMarker != "" || restored["route"].Selected != "node-00" || len(restored["route"].Samples) == 0 {
		t.Fatal("checkpoint mixed warm session proof and cold history")
	}
}

func TestWarmContinuationInvalidatesChangedContextAndColdDegradation(t *testing.T) {
	for _, change := range []string{"fingerprint", "removed", "active", "origin", "runtime", "metric", "unconfirmed", "underlay", "failure", "primary-degraded", "expired"} {
		t.Run(change, func(t *testing.T) {
			c, r, now := warmControllerFixture(t, 2, 1, 1)
			item, contract := c.state["route"], r.pool.HealthPolicies["route"]
			c.prepareWarmPlanning(contract, item)
			item.CandidateLabels = map[string]string{"node-00": "active", "node-01": "reserve"}
			item.qualityPair = newQualityContinuation("node-00", "node-01", "node-01", now, item, true)
			if !pairContinuationValid(now, item) {
				t.Fatal("fresh warm continuation invalid")
			}
			switch change {
			case "fingerprint":
				item.CandidateNodes["node-01"] = healthNode{Fingerprint: "changed"}
			case "removed":
				delete(item.CandidateLabels, "node-01")
			case "active":
				item.Selected = "node-01"
			case "origin":
				item.LatencyMeasurement = "other-origin"
			case "runtime":
				item.WarmPlanningSignature = "new-generation"
			case "metric":
				item.WarmPlanningMarker = ""
			case "unconfirmed":
				item.RuntimeConfirmed = false
			case "underlay":
				item.UnderlayFailure = "wan"
			case "failure":
				item.AvailabilityFailures["node-00"] = 1
			case "primary-degraded":
				item.Failures["node-00"] = 1
				item.Samples["node-00"] = []healthSample{{At: float64(now.Unix()), OK: true, PrimaryQualityFailed: true}}
			case "expired":
				now = now.Add(qualityPairWindow + time.Millisecond)
			}
			if pairContinuationValid(now, item) {
				t.Fatal("stale continuation survived " + change)
			}
		})
	}
}

func TestWarmDegradedRepairUsesColdEvidenceAndReleasesAfterRecovery(t *testing.T) {
	c, r, now := warmControllerFixture(t, 2, 1, 1)
	item, contract := c.state["route"], r.pool.HealthPolicies["route"]
	c.prepareWarmPlanning(contract, item)
	item.CandidateLabels = map[string]string{"node-00": "active", "node-01": "reserve"}
	item.Failures["node-00"] = 1
	item.Samples["node-00"] = []healthSample{{At: float64(now.Unix()), OK: true, PrimaryQualityFailed: true}}
	e := successfulEvidence(100)
	e.ObservedAt = now
	e.QualityStartedAt = now.Add(-time.Second)
	e.Warm = &warmQualityEvidence{Measurement: qualityMeasurementWarm, ObservedAt: now, Failed: true}
	e.PrimaryQualityFailed, e.QualityUnmeasured, e.DelayMS = true, true, nil
	planQualityContinuation(now, "node-00", []string{"node-01"}, []string{"node-00"}, map[string]probeEvidence{"node-00": e}, item, false)
	if item.qualityPair == nil || item.qualityPair.Measurement != qualityMeasurementCold {
		t.Fatal("degraded repair mixed cold samples with warm observation")
	}
	item.Failures["node-00"] = 0
	if pairContinuationValid(now, item) {
		t.Fatal("cold repair survived primary recovery into warm mode")
	}
	item.qualityPair = newQualityContinuation("node-00", "node-01", "node-01", now, item, true)
	e.OK = true
	e.Warm.Deferred = true
	e.Warm.Failed = false
	planQualityContinuation(now, "node-00", nil, []string{"node-01"}, map[string]probeEvidence{"node-01": e}, item, true)
	if item.qualityPair != nil {
		t.Fatal("attempted deferred warm job retained rapid continuation retries")
	}
}

func TestWarmThreeListsOneSlotCompleteInventoryAndKeepLiveness(t *testing.T) {
	c, r, base := warmControllerFixture(t, 24, 1, 1)
	contract := r.pool.HealthPolicies["route"]
	original := c.state["route"]
	delete(r.pool.HealthPolicies, "route")
	delete(c.state, "route")
	delete(r.current, "route")
	c.warmStarted = map[string]bool{}
	for i := 0; i < 3; i++ {
		id := fmt.Sprint("list-", i)
		item := newPolicyHealthState()
		item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "node-00", "node-00", true
		item.Mode, item.LatencyMeasurement, item.CandidateSignature = "best", original.LatencyMeasurement, original.CandidateSignature
		item.CandidateNodes = contract.Nodes
		item.AvailabilityOK, item.QualityOK = map[string]bool{}, map[string]bool{}
		for _, node := range contract.Candidates {
			delay := 200
			item.Samples[node] = []healthSample{{At: 998, OK: true, DelayMS: &delay}}
			item.AvailabilityOK[node], item.QualityOK[node], item.Recoveries[node] = true, true, 3
			item.LastProbeAt[node] = 935
			r.warm[node] = 75
		}
		r.pool.HealthPolicies[id] = contract
		r.current[id] = "node-00"
		c.state[id] = item
		c.warmStarted[id] = true
	}
	r.advanceClock = true
	seen := map[string]map[string]bool{}
	for cycle := 0; r.at.Before(base.Add(400*time.Second)) && cycle < 500; cycle++ {
		before := len(r.probeCalls)
		if err := c.Tick(r.at); err != nil {
			t.Fatal(err)
		}
		if len(r.probeCalls)-before > 1 {
			t.Fatal("three lists exceeded the shared one-slot budget")
		}
		for id, item := range c.state {
			if item.Selected != "node-00" || item.OptimizationChecks != 0 {
				t.Fatal("equal warm paths caused a switch")
			}
			if seen[id] == nil {
				seen[id] = map[string]bool{}
			}
			for node := range item.WarmPlanningLastProbeAt {
				seen[id][node] = true
			}
			refreshDisplayComparisons(r.at, item)
		}
		r.at = r.at.Add(c.nextIntervalAt(r.at))
	}
	for id, nodes := range seen {
		if len(nodes) != 24 {
			t.Fatalf("%s starved inventory: %d/24", id, len(nodes))
		}
	}
}

func TestWarmSharedEarlyResultDoesNotPostponeConfirmation(t *testing.T) {
	c, r, base := warmControllerFixture(t, 2, 2, 2)
	if err := c.Tick(base); err != nil {
		t.Fatal(err)
	}
	item, contract := c.state["route"], r.pool.HealthPolicies["route"]
	if item.OptimizationChecks != 1 {
		t.Fatal("missing first proof")
	}
	clock := item.WarmPlanningLastProbeAt["node-01"]
	c.beginProbeBudget(r.pool, []string{"route"})
	r.at = base.Add(20 * time.Second)
	for node, e := range r.ProbeQualityParallelWithMeasurement(contract.Candidates, qualityMeasurementWarm) {
		c.probeBudget.Quality[c.probeKey("route", node, contract)] = sharedProbeResult{At: time.Now(), Evidence: e}
	}
	result := c.sharedQualityBatch("route", contract, contract.Candidates)
	if !result["node-00"].Deferred || !result["node-01"].Deferred {
		t.Fatal("early shared wave consumed policy producer clocks")
	}
	if item.WarmPlanningLastProbeAt["node-01"] != clock || item.OptimizationChecks != 1 {
		t.Fatal("early cache changed pending confirmation")
	}
}

func TestWarmEmergencyKeepsConfiguredBatchAndDoesNotWarmProbes(t *testing.T) {
	c, r, now := warmControllerFixture(t, 24, 2, 10)
	item := c.state["route"]
	item.AvailabilityFailures[item.Selected] = 3
	for node := range r.probes {
		r.probes[node] = probeEvidence{Failure: probeFailureFatal}
	}
	if err := c.Tick(now); err != nil {
		t.Fatal(err)
	}
	if len(r.availabilityCalls) < 3 || len(r.availabilityCalls) > 10 || item.ProbeLimits.Batch != 10 {
		t.Fatalf("ordinary warm cap changed emergency width: availability=%v limits=%+v", r.availabilityCalls, item.ProbeLimits)
	}
	if len(r.measurements) != 0 {
		t.Fatal("emergency issued planned warm requests")
	}
}

func TestWarmOutagePreemptsPendingContinuation(t *testing.T) {
	c, r, now := warmControllerFixture(t, 2, 1, 1)
	if err := c.Tick(now); err != nil {
		t.Fatal(err)
	}
	item := c.state["route"]
	if item.qualityPair == nil {
		t.Fatal("fixture lacks unfinished pair")
	}
	item.AvailabilityFailures["node-00"] = 3
	r.probes["node-00"] = probeEvidence{Failure: probeFailureFatal}
	r.at = now.Add(time.Second)
	before := len(r.measurements)
	if err := c.Tick(r.at); err != nil {
		t.Fatal(err)
	}
	if item.Selected != "node-01" || item.LastSwitchReason != "active-unavailable" || len(r.measurements) != before {
		t.Fatal("pending quality pair delayed emergency recovery")
	}
}
