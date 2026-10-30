package agentport

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type fractionalDegradedRuntime struct {
	*fakeSelectorRuntime
	mu    sync.Mutex
	clock time.Time
}

func (r *fractionalDegradedRuntime) currentTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clock
}

func (r *fractionalDegradedRuntime) advance(duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = r.clock.Add(duration)
}

func (r *fractionalDegradedRuntime) Probe(node string) probeEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.fakeSelectorRuntime.Probe(node)
	r.clock = r.clock.Add(375 * time.Millisecond)
	e.ObservedAt = r.clock
	return e
}

func (r *fractionalDegradedRuntime) ProbeAvailability(node string) probeEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = r.clock.Add(175 * time.Millisecond)
	delay := 1
	return probeEvidence{OK: true, ObservedAt: r.clock, Targets: map[string]*int{"cloudflare-trace": &delay}}
}

func TestSixDegradedListsKeepFractionalCompletionTime(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		t.Run(mode, func(t *testing.T) {
			base := healthFixture(false).HealthPolicies["europe"]
			pool := healthPool{Version: 4, ProbeBudget: 1, HealthPolicies: map[string]healthPolicyContract{}}
			fake := &fakeSelectorRuntime{current: map[string]string{}, probes: map[string]probeEvidence{}}
			runtime := &fractionalDegradedRuntime{fakeSelectorRuntime: fake, clock: time.Unix(1000, 173000000)}
			controller := &healthController{opts: Options{StateRoot: t.TempDir(), HealthInterval: time.Minute}, runtime: runtime, stateLoaded: true, state: healthState{}, warmStarted: map[string]bool{}}
			for n := 0; n < 6; n++ {
				id := fmt.Sprintf("list-%d", n)
				a, b := id+"-active", id+"-reserve"
				contract := base
				contract.Mode, contract.Groups = mode, nil
				contract.Candidates, contract.Nodes = []string{a, b}, budgetContract(a, b).Nodes
				contract.Policy.ActiveCheckSeconds, contract.Policy.ActiveLivenessSeconds, contract.Policy.ProbeBatchSize = 60, 3, 1
				contract.Policy.LatencyMeasurement = "gstatic-head-v1"
				pool.HealthPolicies[id] = contract
				item := newPolicyHealthState()
				item.Mode, item.LatencyMeasurement = mode, contract.Policy.LatencyMeasurement
				item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = a, a, true
				item.CandidateSignature, item.CandidateNodes = a+"\n"+b, contract.Nodes
				item.AvailabilityOK, item.QualityOK = map[string]bool{a: true, b: true}, map[string]bool{a: true, b: true}
				for _, node := range []string{a, b} {
					item.LastProbeAt[node] = qualityTimestamp(runtime.currentTime().Add(-time.Minute), probeEvidence{})
					item.Recoveries[node] = 3
					at := runtime.currentTime().Add(-time.Duration(6-n) * time.Second)
					delay := 125
					item.Samples[node] = []healthSample{{At: qualityTimestamp(at, probeEvidence{}), OK: true, DelayMS: &delay}}
				}
				fake.probes[a] = probeEvidence{OK: true, QualityUnmeasured: true, PrimaryQualityFailed: true}
				fake.probes[b] = successfulEvidence(125)
				fake.current[id], controller.state[id], controller.warmStarted[id] = a, item, true
			}
			fake.pool = pool
			until := runtime.currentTime().Add(4 * time.Minute)
			for cycle := 0; cycle < 500 && runtime.currentTime().Before(until); cycle++ {
				before := len(fake.probeCalls)
				if err := controller.Tick(runtime.currentTime()); err != nil {
					t.Fatal(err)
				}
				if len(fake.probeCalls)-before > 1 {
					t.Fatal("shared quality budget exceeded")
				}
				all := true
				for id, item := range controller.state {
					all = all && item.Selected == id+"-reserve"
				}
				if all {
					for id, item := range controller.state {
						at, err := time.Parse(time.RFC3339Nano, item.CheckedAt)
						if err != nil || at.Nanosecond() == 0 {
							t.Fatalf("%s lost completion precision: %q", id, item.CheckedAt)
						}
					}
					return
				}
				runtime.advance(max(time.Millisecond, controller.nextIntervalAt(runtime.currentTime())))
			}
			for id, item := range controller.state {
				if item.Selected != id+"-reserve" {
					t.Errorf("%s starved: failures=%d next=%v pair=%+v", id, item.Failures[item.Selected], controller.regularNext[id], item.qualityPair)
				}
			}
		})
	}
}
