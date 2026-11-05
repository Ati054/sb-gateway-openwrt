package agentport

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWarmInitialPairsConfirmWithoutThirdQualityJob(t *testing.T) {
	for _, count := range []int{1, 3, 6} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c, r, base := warmControllerFixture(t, 2, 1, 1)
			contract := r.pool.HealthPolicies["route"]
			delete(c.state, "route")
			delete(r.pool.HealthPolicies, "route")
			delete(r.current, "route")
			c.warmStarted = map[string]bool{}
			for i := 0; i < count; i++ {
				id := fmt.Sprint("list-", i)
				item := newPolicyHealthState()
				item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "node-00", "node-00", true
				item.Mode, item.LatencyMeasurement, item.CandidateSignature = "best", contract.Policy.LatencyMeasurement, strings.Join(contract.Candidates, "\n")
				item.CandidateNodes = contract.Nodes
				item.AvailabilityOK, item.QualityOK = map[string]bool{}, map[string]bool{}
				for _, node := range contract.Candidates {
					delay := *r.probes[node].DelayMS
					item.Samples[node] = []healthSample{{At: 998, OK: true, DelayMS: &delay}}
					item.LastProbeAt[node], item.Recoveries[node] = 935, 3
					item.AvailabilityOK[node], item.QualityOK[node] = true, true
				}
				c.state[id], c.warmStarted[id] = item, true
				r.pool.HealthPolicies[id], r.current[id] = contract, "node-00"
			}
			r.advanceClock = true
			for cycle := 0; cycle < 12*count && r.at.Before(base.Add(30*time.Second)); cycle++ {
				before := len(r.probeCalls)
				if err := c.Tick(r.at); err != nil {
					t.Fatal(err)
				}
				if len(r.probeCalls)-before > 1 {
					t.Fatal("one-slot budget exceeded")
				}
				all := true
				for id, item := range c.state {
					if item.Selected != "node-00" || item.OptimizationChecks > 1 {
						t.Fatalf("premature switch/proof for %s", id)
					}
					all = all && item.OptimizationChecks == 1
				}
				if all {
					if len(r.probeCalls) > 2*count {
						t.Fatalf("first pairs spent extra quality jobs: %v", r.probeCalls)
					}
					return
				}
				if len(r.probeCalls) > 2*count {
					t.Fatalf("first pair needed a third quality job: %v", r.probeCalls)
				}
				r.at = r.at.Add(c.nextIntervalAt(r.at))
			}
			t.Fatalf("initial pairs did not complete: calls=%v", r.probeCalls)
		})
	}
}
