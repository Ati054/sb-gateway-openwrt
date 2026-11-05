package agentport

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWarmSixDisjointListsShareOneSlotWithoutStarvingActiveProofs(t *testing.T) {
	c, r, base := warmControllerFixture(t, 24, 1, 1)
	delete(c.state, "route")
	delete(r.pool.HealthPolicies, "route")
	delete(r.current, "route")
	c.warmStarted = map[string]bool{}
	for p := 0; p < 6; p++ {
		id := fmt.Sprint("list-", p)
		contract := healthPolicyContract{Mode: "best", Nodes: map[string]healthNode{}, Policy: healthPolicy{ActiveCheckSeconds: 60, ActiveLivenessSeconds: 3, ProbeBatchSize: 1, SwitchImprovementMS: 200}}
		item := newPolicyHealthState()
		item.AvailabilityOK, item.QualityOK = map[string]bool{}, map[string]bool{}
		for n := 0; n < 24; n++ {
			node := fmt.Sprintf("%s-node-%02d", id, n)
			contract.Candidates = append(contract.Candidates, node)
			contract.Nodes[node] = healthNode{Label: node, Fingerprint: node}
			r.probes[node] = successfulEvidence(200)
			r.warm[node] = 75
			delay := 200
			item.Samples[node] = []healthSample{{At: 998, OK: true, DelayMS: &delay}}
			item.LastProbeAt[node], item.Recoveries[node] = 935, 3
			item.AvailabilityOK[node], item.QualityOK[node] = true, true
		}
		item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = contract.Candidates[0], contract.Candidates[0], true
		item.Mode, item.CandidateSignature, item.CandidateNodes = "best", strings.Join(contract.Candidates, "\n"), contract.Nodes
		c.state[id] = item
		c.warmStarted[id] = true
		r.current[id] = item.Selected
		r.pool.HealthPolicies[id] = contract
	}
	r.advanceClock = true
	// At least 276 physical jobs are needed for 6 * 23 active/reserve pairs;
	// each consumes 575 ms, its continuation wake, and up to six liveness jobs.
	for cycle := 0; cycle < 1200 && r.at.Before(base.Add(650*time.Second)); cycle++ {
		before := len(r.probeCalls)
		if err := c.Tick(r.at); err != nil {
			t.Fatal(err)
		}
		if len(r.probeCalls)-before > 1 {
			t.Fatal("quality job budget exceeded")
		}
		r.at = r.at.Add(c.nextIntervalAt(r.at))
	}
	for id, item := range c.state {
		if item.Selected != r.pool.HealthPolicies[id].Candidates[0] {
			t.Fatal("equal paths caused a route change")
		}
		if len(item.WarmPlanningLastProbeAt) != 24 {
			t.Fatalf("%s checked %d/24 endpoints", id, len(item.WarmPlanningLastProbeAt))
		}
		proofs := append(append([]time.Time{}, r.attempts[item.Selected]...), r.availabilityAt[item.Selected]...)
		slices.SortFunc(proofs, func(a, b time.Time) int { return a.Compare(b) })
		maxGap := time.Duration(0)
		for i := 1; i < len(proofs); i++ {
			maxGap = max(maxGap, proofs[i].Sub(proofs[i-1]))
		}
		if len(proofs) < 3 || maxGap > 10*time.Second {
			t.Fatalf("%s active proof gap %s, count=%d", id, maxGap, len(proofs))
		}
		for _, node := range r.pool.HealthPolicies[id].Candidates[1:] {
			if len(r.attempts[node]) == 0 {
				t.Fatal("reserve starved: " + node)
			}
		}
	}
	if r.heads != 2*len(r.probeCalls) {
		t.Fatalf("unexpected primary requests: %d for %d jobs", r.heads, len(r.probeCalls))
	}
	t.Logf("six lists,144 endpoints: %d quality jobs, %d primary requests in %s simulated time", len(r.probeCalls), r.heads, r.at.Sub(base))
}
