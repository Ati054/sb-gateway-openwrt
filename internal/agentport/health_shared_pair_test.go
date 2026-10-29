package agentport

import (
	"testing"
	"time"
)

func TestSharedComparisonDefersIncoherentCacheWithoutRequests(t *testing.T) {
	for _, scenario := range []string{"spread", "old-and-new", "failed-peer"} {
		t.Run(scenario, func(t *testing.T) {
			runtime := &observedQualityRuntime{&fakeSelectorRuntime{probes: map[string]probeEvidence{"a": successfulEvidence(600), "b": successfulEvidence(400)}}}
			c := &healthController{runtime: runtime}
			c.beginProbeBudget(healthPool{ProbeBudget: 2}, nil)
			contract := budgetContract("a", "b")
			contract.Mode = "best"
			now := time.Now()
			store := func(node string, at time.Time) {
				evidence := runtime.probes[node]
				evidence.ObservedAt = at
				key := c.probeKey("first", node, contract)
				c.probeBudget.Quality[key] = sharedProbeResult{At: at, Evidence: evidence}
				c.probeBudget.QualityAttempted[key] = true
			}
			switch scenario {
			case "spread":
				store("a", now.Add(-12*time.Second))
				store("b", now.Add(-time.Second))
			case "old-and-new":
				store("a", now.Add(-11*time.Second))
			case "failed-peer":
				c.probeBudget.QualityAttempted[c.probeKey("first", "a", contract)] = true
			}
			accepted, deferred := c.claimProbeTargets("second", contract, []string{"a", "b"}, false)
			if len(accepted) != 0 || len(deferred) != 2 || len(c.probeBudget.Used) != 0 {
				t.Fatalf("incoherent pair consumed a slot: accepted=%v deferred=%v used=%v", accepted, deferred, c.probeBudget.Used)
			}
			// The batch entry point must also be safe when called directly.
			results := c.sharedQualityBatch("second", contract, []string{"a", "b"})
			if len(runtime.probeCalls) != 0 || !results["a"].Deferred || !results["b"].Deferred {
				t.Fatalf("incoherent batch launched requests or leaked half a pair: calls=%v results=%+v", runtime.probeCalls, results)
			}
		})
	}
}

func TestSharedComparisonKeepsCoherentCachedPairs(t *testing.T) {
	for _, gap := range []time.Duration{time.Second, qualityPairWindow} {
		t.Run(gap.String(), func(t *testing.T) {
			runtime := &observedQualityRuntime{&fakeSelectorRuntime{}}
			c := &healthController{runtime: runtime}
			c.beginProbeBudget(healthPool{ProbeBudget: 1}, nil)
			contract := budgetContract("a", "b")
			contract.Mode = "best"
			now := time.Now()
			for i, node := range []string{"a", "b"} {
				evidence := successfulEvidence(600 - 200*i)
				evidence.ObservedAt = now.Add(-12*time.Second + time.Duration(i)*gap)
				key := c.probeKey("first", node, contract)
				c.probeBudget.Quality[key] = sharedProbeResult{At: evidence.ObservedAt, Evidence: evidence}
				c.probeBudget.QualityAttempted[key] = true
			}
			accepted, deferred := c.claimProbeTargets("second", contract, []string{"a", "b"}, false)
			results := c.sharedQualityBatch("second", contract, accepted)
			if len(accepted) != 2 || len(deferred) != 0 || len(runtime.probeCalls) != 0 || len(c.probeBudget.Used) != 0 || !results["a"].OK || !results["b"].OK {
				t.Fatalf("coherent shared pair was discarded or charged twice: accepted=%v deferred=%v calls=%v results=%+v", accepted, deferred, runtime.probeCalls, results)
			}
		})
	}
}

func TestSharedComparisonKeepsFreshCacheAndPriorityEvidence(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			runtime := &observedQualityRuntime{&fakeSelectorRuntime{probes: map[string]probeEvidence{"b": successfulEvidence(400)}}}
			c := &healthController{runtime: runtime}
			c.beginProbeBudget(healthPool{ProbeBudget: 1}, nil)
			contract := budgetContract("a", "b")
			contract.Mode = mode
			at := now.Add(-time.Second)
			if mode == "priority" {
				at = now.Add(-11 * time.Second)
			}
			evidence := successfulEvidence(600)
			evidence.ObservedAt = at
			key := c.probeKey("first", "a", contract)
			c.probeBudget.Quality[key] = sharedProbeResult{At: at, Evidence: evidence}
			c.probeBudget.QualityAttempted[key] = true
			accepted, deferred := c.claimProbeTargets("second", contract, []string{"a", "b"}, false)
			results := c.sharedQualityBatch("second", contract, accepted)
			if len(deferred) != 0 || len(runtime.probeCalls) != 1 || runtime.probeCalls[0] != "b" || !results["a"].ObservedAt.Equal(at) || !results["a"].OK || !results["b"].OK {
				t.Fatalf("valid raw evidence was lost or refreshed: deferred=%v calls=%v results=%+v", deferred, runtime.probeCalls, results)
			}
		})
	}
}
