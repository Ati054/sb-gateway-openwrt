package agentport

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

type observedClockRuntime struct {
	*fakeSelectorRuntime
	clock time.Time
}

func (r *observedClockRuntime) Probe(node string) probeEvidence {
	r.clock = r.clock.Add(4 * time.Second)
	e := r.fakeSelectorRuntime.Probe(node)
	e.ObservedAt = r.clock
	return e
}

func TestSixPoliciesKeepIndividualCompletionIntervals(t *testing.T) {
	c, base, _ := livenessFixture(t, "best")
	contract := base.pool.HealthPolicies["europe"]
	contract.Groups = nil
	contract.Policy.ProbeBatchSize = 2
	base.pool.HealthPolicies = map[string]healthPolicyContract{}
	base.current = map[string]string{}
	c.state = healthState{}
	c.warmStarted = map[string]bool{}
	c.regularNext = map[string]time.Time{}
	start := time.Unix(1060, 0)
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("route-%d", i)
		item := newPolicyHealthState()
		item.Selected = "de"
		item.RuntimeSelected = "de"
		item.RuntimeConfirmed = true
		item.Mode = "best"
		item.CandidateSignature = strings.Join(contract.Candidates, "\n")
		for _, node := range contract.Candidates {
			item.LastProbeAt[node] = 1000
			item.AvailabilityOK = map[string]bool{"de": true, "nl": true}
			item.QualityOK = map[string]bool{"de": true, "nl": true}
		}
		base.pool.HealthPolicies[id] = contract
		base.current[id] = "de"
		c.state[id] = item
		c.warmStarted[id] = true
	}
	base.probeCalls = nil
	c.runtime = &observedClockRuntime{base, start}
	if err := c.Tick(start); err != nil {
		t.Fatal(err)
	}
	if len(base.probeCalls) != 12 {
		t.Fatalf("six pairs did not run: %v", base.probeCalls)
	}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("route-%d", i)
		observed := start.Add(time.Duration(i*8+4) * time.Second)
		if c.state[id].LastProbeAt["de"] != float64(observed.Unix()) || c.regularNext[id].Before(observed.Add(time.Minute)) {
			t.Fatalf("%s scheduled from common cycle start", id)
		}
	}
	if err := c.Tick(start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(base.probeCalls) != 12 {
		t.Fatalf("late policies remeasured too early: %v", base.probeCalls)
	}
}

func TestLongQualityBatchDoesNotConsumeNextProbeInterval(t *testing.T) {
	for _, duration := range []time.Duration{52 * time.Second, 68 * time.Second, 128 * time.Second} {
		t.Run(fmt.Sprint(duration), func(t *testing.T) {
			c, runtime, item := livenessFixture(t, "best")
			start := time.Unix(2000, 0)
			finished := start.Add(duration)
			activeFinished := finished.Add(-6 * time.Second)
			contract := runtime.pool.HealthPolicies["europe"]
			contract.Policy.ProbeBatchSize = 2
			runtime.pool.HealthPolicies["europe"] = contract
			for node, delay := range map[string]int{"de": 100, "nl": 90} {
				evidence := successfulEvidence(delay)
				evidence.ObservedAt = finished
				if node == "de" {
					evidence.ObservedAt = activeFinished
				}
				runtime.probes[node] = evidence
				item.LastProbeAt[node] = float64(start.Unix() - 60)
			}
			c.regularNext["europe"] = start
			runtime.probeCalls = nil
			if err := c.Tick(start); err != nil {
				t.Fatal(err)
			}
			if len(runtime.probeCalls) != 2 {
				t.Fatalf("incomplete batch: %v", runtime.probeCalls)
			}
			if item.LastProbeAt["de"] != float64(activeFinished.Unix()) || item.LastProbeAt["nl"] != float64(finished.Unix()) {
				t.Fatal("batch start replaced individual completion timestamps")
			}
			due := activeFinished.Add(time.Minute)
			if !c.regularNext["europe"].Equal(due) {
				t.Fatalf("next quality=%s, want %s", c.regularNext["europe"], due)
			}
			availabilityBefore := len(runtime.availabilityCalls)
			for _, at := range []time.Time{finished.Add(6 * time.Second), due.Add(-time.Second)} {
				runtime.probeCalls = nil
				if err := c.Tick(at); err != nil {
					t.Fatal(err)
				}
				if len(runtime.probeCalls) != 0 {
					t.Fatalf("long batch caused an early quality probe: %v", runtime.probeCalls)
				}
			}
			if len(runtime.availabilityCalls) <= availabilityBefore {
				t.Fatal("quality cooldown suppressed fast availability checks")
			}
			evidence := successfulEvidence(100)
			evidence.ObservedAt = due
			runtime.probes["de"] = evidence
			runtime.probeCalls = nil
			if err := c.Tick(due); err != nil {
				t.Fatal(err)
			}
			if len(runtime.probeCalls) == 0 || runtime.probeCalls[0] != "de" {
				t.Fatalf("due active probe was delayed: %v", runtime.probeCalls)
			}
		})
	}
}
