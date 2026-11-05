package agentport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func disjointWarmScanFixture(t *testing.T, policies int) (*healthController, *warmPlanningTestRuntime, time.Time) {
	t.Helper()
	controller, runtime, base := warmControllerFixture(t, 2, 1, 1)
	controller.opts.HealthInterval = time.Minute
	runtime.advanceClock = true
	template := runtime.pool.HealthPolicies["route"]
	seed, _ := json.Marshal(controller.state["route"])
	controller.state, controller.warmStarted = healthState{}, map[string]bool{}
	runtime.pool.HealthPolicies, runtime.current = map[string]healthPolicyContract{}, map[string]string{}
	runtime.probes, runtime.warm = map[string]probeEvidence{}, map[string]int{}
	for policy := 0; policy < policies; policy++ {
		id := fmt.Sprintf("disjoint-%d", policy)
		contract := template
		contract.Candidates = make([]string, 2)
		contract.Nodes = map[string]healthNode{}
		for node, original := range template.Candidates {
			name := id + "-" + original
			contract.Candidates[node] = name
			contract.Nodes[name] = healthNode{Fingerprint: "synthetic-" + name, Protocol: "vless", Transport: "ws"}
			runtime.probes[name], runtime.warm[name] = successfulEvidence(200+node*200), 75+node*50
		}
		item := newPolicyHealthState()
		if err := json.Unmarshal(seed, item); err != nil {
			t.Fatal(err)
		}
		item.Selected, item.RuntimeSelected = contract.Candidates[0], contract.Candidates[0]
		item.CandidateSignature, item.CandidateNodes = strings.Join(contract.Candidates, "\n"), contract.Nodes
		item.Samples = prefixWarmModelMap(item.Samples, id+"-")
		item.LastProbeAt = prefixWarmModelMap(item.LastProbeAt, id+"-")
		item.Recoveries = prefixWarmModelMap(item.Recoveries, id+"-")
		item.QualityOK = prefixWarmModelMap(item.QualityOK, id+"-")
		item.AvailabilityOK = prefixWarmModelMap(item.AvailabilityOK, id+"-")
		controller.state[id], controller.warmStarted[id] = item, true
		runtime.pool.HealthPolicies[id], runtime.current[id] = contract, item.Selected
	}
	return controller, runtime, base
}

func prefixWarmModelMap[T any](input map[string]T, prefix string) map[string]T {
	result := make(map[string]T, len(input))
	for key, value := range input {
		result[prefix+key] = value
	}
	return result
}

func TestFreshPlanningMetricIsolationAndContextReset(t *testing.T) {
	controller, _, base := warmControllerFixture(t, 2, 2, 2)
	contract, item := controller.runtime.(*warmPlanningTestRuntime).pool.HealthPolicies["route"], controller.state["route"]
	controller.prepareWarmPlanning(contract, item)
	oldKey := controller.probeKey("route", "node-00", contract)
	oldSignature := item.WarmPlanningSignature
	item.OptimizationCandidate, item.OptimizationBaseline, item.OptimizationChecks = "node-01", item.Selected, 1
	item.OptimizationLastResult = &optimizationComparison{Result: optimizationWin, At: base.Format(time.RFC3339)}
	selected, cold := item.Selected, item.Samples["node-00"][0]
	controller.opts.URLTestPlanningMode = URLTestPlanningFreshPaired
	controller.prepareWarmPlanning(contract, item)
	if item.WarmPlanningMarker != string(qualityMeasurementFreshPaired) || oldSignature == item.WarmPlanningSignature ||
		oldKey == controller.probeKey("route", "node-00", contract) || item.OptimizationChecks != 0 || item.OptimizationLastResult != nil ||
		item.Selected != selected || item.Samples["node-00"][0].At != cold.At {
		t.Fatal("mode transition mixed proof/cache identity or lost working route/cold history")
	}
	delay := 100
	value := &planningQualityEvidence{Measurement: qualityMeasurementFreshPaired, StartedAt: base, ObservedAt: base.Add(time.Second), DelayMS: &delay}
	if validWarmQuality(value) || !validPlanningQuality(value, qualityMeasurementFreshPaired) || validPlanningQuality(value, qualityMeasurementWarm) {
		t.Fatal("fresh observation was relabelled as strict same-connection quality")
	}
	for _, wrong := range []qualityMeasurement{qualityMeasurementWarm, qualityMeasurementCold, "unknown"} {
		copy := *value
		copy.Measurement = wrong
		collectWarmPlanning(base.Add(time.Second), []string{"node-00"}, map[string]probeEvidence{"node-00": {OK: true, Planning: &copy}}, item, policySettings(contract.Policy, contract.Mode))
		if len(item.WarmPlanningSamples["node-00"]) != 0 {
			t.Fatal("foreign metric entered planning history")
		}
	}
	controller.opts.URLTestPlanningMode = URLTestPlanningLegacy
	controller.prepareWarmPlanning(contract, item)
	if item.WarmPlanningMarker != "" || item.Selected != selected || len(item.Samples["node-00"]) == 0 {
		t.Fatal("legacy transition retained planning evidence or discarded cold history")
	}
}

func TestFreshPlanningDisjointOneSlotFairness(t *testing.T) {
	for _, policies := range []int{3, 6} {
		t.Run(fmt.Sprint(policies), func(t *testing.T) {
			controller, runtime, base := disjointWarmScanFixture(t, policies)
			controller.opts.URLTestPlanningMode = URLTestPlanningFreshPaired
			now := base
			for cycles := 0; len(runtime.probeCalls) < 2*policies && cycles < 8*policies; cycles++ {
				runtime.at = now
				before := len(runtime.probeCalls)
				if err := controller.Tick(now); err != nil {
					t.Fatal(err)
				}
				if len(runtime.probeCalls)-before > 1 {
					t.Fatal("fresh pair escaped one-slot budget")
				}
				now = runtime.at.Add(controller.nextIntervalAt(runtime.at))
			}
			if len(runtime.probeCalls) != 2*policies || runtime.heads != 2*policies {
				t.Fatalf("fresh survey stalled or added HTTP: jobs=%d primary=%d", len(runtime.probeCalls), runtime.heads)
			}
			for id, item := range controller.state {
				contract := runtime.pool.HealthPolicies[id]
				pairs := currentLatencyComparisons(now, warmPlanningView(item), contract.Candidates, policySettings(contract.Policy, contract.Mode))
				if len(pairs) != 1 {
					t.Fatalf("policy %s did not finish its first qualified pair", id)
				}
			}
		})
	}
}

func TestFreshAndPriorityUseDistinctSharedCacheEntries(t *testing.T) {
	controller, runtime, _ := warmControllerFixture(t, 2, 2, 4)
	controller.opts.URLTestPlanningMode = URLTestPlanningFreshPaired
	runtime.at = time.Now()
	controller.warmStarted = map[string]bool{"best": true, "priority": true}
	controller.beginProbeBudget(healthPool{ProbeBudget: 4}, []string{"best", "priority"})
	contract := runtime.pool.HealthPolicies["route"]
	for repeat := 0; repeat < 2; repeat++ {
		for _, mode := range []string{"best", "priority"} {
			contract.Mode = mode
			evidence := controller.sharedQualityProbe(mode, "node-00", contract)
			if !evidence.OK {
				t.Fatal("shared synthetic request failed")
			}
			if mode == "best" && !validPlanningQuality(evidence.Planning, qualityMeasurementFreshPaired) {
				t.Fatal("fresh cache returned priority evidence")
			}
			if mode == "priority" && evidence.Planning != nil {
				t.Fatal("priority cache returned fresh planning evidence")
			}
		}
	}
	if runtime.heads != 2 || len(runtime.probeCalls) != 2 || len(controller.probeBudget.Quality) != 2 {
		t.Fatalf("cache identity/cost mismatch: heads=%d jobs=%v", runtime.heads, runtime.probeCalls)
	}
}
