package agentport

import (
	"fmt"
	"testing"
	"time"
)

func TestFreshPlanningModesAndIndependentAvailability(t *testing.T) {
	for _, mode := range []URLTestPlanningMode{URLTestPlanningLegacy, URLTestPlanningWarmPaired, URLTestPlanningFreshPaired} {
		if err := (Options{URLTestPlanningMode: mode}).validatePlanningMode(); err != nil {
			t.Fatal(err)
		}
	}
	if err := (Options{URLTestPlanningMode: "fresh"}).validatePlanningMode(); err == nil {
		t.Fatal("invalid metric accepted")
	}
	for _, scenario := range []string{"startup", "priority"} {
		t.Run(scenario, func(t *testing.T) {
			c, r, now := warmControllerFixture(t, 2, 2, 2)
			c.opts.URLTestPlanningMode = URLTestPlanningFreshPaired
			if scenario == "startup" {
				c.warmStarted["route"] = false
			} else {
				contract := r.pool.HealthPolicies["route"]
				contract.Mode = "priority"
				r.pool.HealthPolicies["route"] = contract
			}
			if err := c.Tick(now); err != nil {
				t.Fatal(err)
			}
			for _, metric := range r.measurements {
				if metric != qualityMeasurementCold {
					t.Fatalf("%s used planning metric", scenario)
				}
			}
		})
	}
}

func TestFreshPlanningTwoIndependentProductionCadenceProofs(t *testing.T) {
	for _, lanes := range []int{1, 2} {
		t.Run(fmt.Sprint(lanes), func(t *testing.T) {
			c, r, base := warmControllerFixture(t, 2, lanes, lanes)
			c.opts.URLTestPlanningMode, c.opts.HealthInterval = URLTestPlanningFreshPaired, time.Minute
			r.advanceClock = true
			r.probes["node-00"], r.probes["node-01"] = successfulEvidence(900), successfulEvidence(100)
			var switches []healthEvent
			c.eventSink = func(event healthEvent) {
				if event.Event == "switch" {
					switches = append(switches, event)
				}
			}
			now, firstCandidateAt := base, float64(0)
			for cycles := 0; now.Before(base.Add(180*time.Second)) && cycles < 180; cycles++ {
				r.at = now
				before := len(r.probeCalls)
				if err := c.Tick(now); err != nil {
					t.Fatal(err)
				}
				if len(r.probeCalls)-before > lanes {
					t.Fatal("planning exceeded the common job budget")
				}
				item := c.state["route"]
				if item.OptimizationChecks == 1 && firstCandidateAt == 0 {
					firstCandidateAt = item.OptimizationLastResult.CandidateAt
				}
				if now.Before(base.Add(time.Minute)) && item.Selected != "node-00" {
					t.Fatal("early proof switched route")
				}
				now = r.at.Add(c.nextIntervalAt(r.at))
			}
			if len(switches) != 1 || c.state["route"].Selected != "node-01" {
				t.Fatalf("fresh planning did not converge: %+v", switches)
			}
			comparison := switches[0].Comparison
			if comparison == nil || comparison.Measurement != string(qualityMeasurementFreshPaired) || firstCandidateAt == 0 || comparison.CandidateAt-firstCandidateAt < 60 {
				t.Fatalf("switch reused a proof or ignored the interval: %+v", comparison)
			}
			if r.heads != len(r.probeCalls) {
				t.Fatal("fresh mode issued an extra planning request")
			}
		})
	}
}

func TestFreshPlanningTransientAndSharedWANLatencyDoNotSwitch(t *testing.T) {
	c, r, base := warmControllerFixture(t, 2, 2, 2)
	c.opts.URLTestPlanningMode = URLTestPlanningFreshPaired
	for second := 0; second <= 240; second++ {
		r.at = base.Add(time.Duration(second) * time.Second)
		active, reserve := 200, 210
		if second < 2 {
			active = 1300
		}
		if second >= 60 && second < 125 {
			active += 1200
			reserve += 1200
		}
		r.probes["node-00"], r.probes["node-01"] = successfulEvidence(active), successfulEvidence(reserve)
		if err := c.Tick(r.at); err != nil {
			t.Fatal(err)
		}
		if c.state["route"].Selected != "node-00" {
			t.Fatalf("transient/common WAN delay switched the route at %d", second)
		}
	}
}

func TestFreshPlanningAllDisjointListsReachSecondProof(t *testing.T) {
	for _, count := range []int{3, 6} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c, r, base := disjointWarmScanFixture(t, count)
			c.opts.URLTestPlanningMode = URLTestPlanningFreshPaired
			firstProof := map[string]float64{}
			for _, contract := range r.pool.HealthPolicies {
				r.probes[contract.Candidates[0]], r.probes[contract.Candidates[1]] = successfulEvidence(900), successfulEvidence(100)
			}
			now := base
			for cycles := 0; cycles < 500 && now.Before(base.Add(300*time.Second)); cycles++ {
				r.at = now
				before := len(r.probeCalls)
				if err := c.Tick(now); err != nil {
					t.Fatal(err)
				}
				if len(r.probeCalls)-before > 1 {
					t.Fatal("one-slot budget exceeded")
				}
				complete := true
				for id, item := range c.state {
					if item.OptimizationChecks == 1 && firstProof[id] == 0 {
						firstProof[id] = item.OptimizationLastResult.CandidateAt
					}
					if item.Selected != r.pool.HealthPolicies[id].Candidates[1] {
						complete = false
						continue
					}
					if firstProof[id] == 0 || item.OptimizationLastResult.CandidateAt-firstProof[id] < 60 {
						t.Fatalf("%s reused its first proof", id)
					}
				}
				if complete {
					return
				}
				now = r.at.Add(c.nextIntervalAt(r.at))
			}
			for id, item := range c.state {
				if item.Selected != r.pool.HealthPolicies[id].Candidates[1] {
					t.Errorf("%s starved: candidate=%s checks=%d", id, item.OptimizationCandidate, item.OptimizationChecks)
				}
			}
		})
	}
}
