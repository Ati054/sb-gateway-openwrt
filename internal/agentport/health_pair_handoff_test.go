package agentport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type cancellableActiveTestRuntime struct {
	*fakeSelectorRuntime
	started, completed, joined chan struct{}
}

func (r *cancellableActiveTestRuntime) ProbeAvailability(node string) probeEvidence {
	close(r.started)
	<-r.completed
	close(r.joined)
	return probeEvidence{Failure: probeFailureTimeout}
}

func (r *cancellableActiveTestRuntime) ProbeAvailabilityContext(ctx context.Context, node string) probeEvidence {
	close(r.started)
	<-ctx.Done()
	close(r.joined)
	return probeEvidence{Failure: probeFailureTimeout}
}

func TestCompletedQualityCancelsRoutineActiveCheckWithoutFailure(t *testing.T) {
	c, base, _ := callbackOrderFixture(t, "best")
	delete(base.pool.HealthPolicies, "z-target")
	delete(c.state, "z-target")
	done := make(chan struct{})
	primary := &cancellableActiveTestRuntime{base, make(chan struct{}), done, make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runtime := &responsiveSelectorRuntime{selectorRuntime: primary, ctx: ctx, backgroundDone: done}
	c.runtime = runtime
	go func() {
		<-primary.started
		close(done)
	}()
	if err := c.checkDuringProbe(time.Unix(1040, 0), base.pool); err != nil {
		t.Fatalf("completed quality was treated as an active failure: %v", err)
	}
	select {
	case <-primary.joined:
	default:
		t.Fatal("canceled active probe still owns its selector")
	}
	item := c.state["a-healthy"]
	if item.AvailabilityFailures["healthy"] != 0 || item.FailureClass["healthy"] != "" ||
		!c.livenessAt["a-healthy"].Equal(time.Unix(1000, 0)) || !item.RuntimeConfirmed {
		t.Fatalf("canceled routine proof changed health: failures=%v class=%v checked=%v", item.AvailabilityFailures, item.FailureClass, c.livenessAt)
	}
}

type delayedOtherPolicyRuntime struct {
	*fakeSelectorRuntime
	now    time.Time
	events []string
}

func (*delayedOtherPolicyRuntime) qualityProbeParallelism() int { return 1 }

func (r *delayedOtherPolicyRuntime) Probe(node string) probeEvidence {
	r.events = append(r.events, "quality:"+node)
	e := r.fakeSelectorRuntime.Probe(node)
	r.now = r.now.Add(2 * time.Second)
	e.ObservedAt = r.now
	return e
}

func (r *delayedOtherPolicyRuntime) ProbeAvailability(node string) probeEvidence {
	r.events = append(r.events, "availability:"+node)
	r.now = r.now.Add(11 * time.Second)
	return r.fakeSelectorRuntime.ProbeAvailability(node)
}

func singleLaneSixPolicyFixture(t *testing.T) (*healthController, *fakeSelectorRuntime, *delayedOtherPolicyRuntime) {
	t.Helper()
	c, base, _ := callbackOrderFixture(t, "best")
	contract := base.pool.HealthPolicies["a-healthy"]
	base.pool.HealthPolicies, base.current = map[string]healthPolicyContract{}, map[string]string{}
	base.pool.ProbeBudget = 1
	c.state, c.warmStarted, c.regularNext, c.livenessAt = healthState{}, map[string]bool{}, map[string]time.Time{}, map[string]time.Time{}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("route-%d", i)
		active, reserve := id+"-active", id+"-reserve"
		policy := contract
		policy.Mode, policy.Groups = "best", nil
		policy.Policy.ProbeBatchSize, policy.Policy.ActiveLivenessSeconds = 1, 30
		policy.Candidates, policy.Nodes = []string{active, reserve}, budgetContract(active, reserve).Nodes
		base.pool.HealthPolicies[id], base.current[id] = policy, active
		item := newPolicyHealthState()
		item.Selected, item.RuntimeSelected, item.RuntimeConfirmed, item.Mode = active, active, true, "best"
		item.CandidateSignature, item.CandidateNodes = strings.Join(policy.Candidates, "\n"), policy.Nodes
		item.AvailabilityOK, item.QualityOK = map[string]bool{}, map[string]bool{}
		for node, delay := range map[string]int{active: 300, reserve: 500} {
			value := delay
			item.Samples[node] = []healthSample{{At: 800, OK: true, DelayMS: &value}}
			item.LastProbeAt[node], item.Recoveries[node] = 800, 3
			item.AvailabilityOK[node], item.QualityOK[node] = true, true
			base.probes[node] = successfulEvidence(delay)
		}
		c.state[id], c.warmStarted[id] = item, true
	}
	r := &delayedOtherPolicyRuntime{fakeSelectorRuntime: base, now: time.Unix(1000, 0)}
	c.runtime = r
	return c, base, r
}

func TestSingleLanePairRunsBeforeSlowOtherLists(t *testing.T) {
	c, base, r := singleLaneSixPolicyFixture(t)
	seen := map[string]bool{}
	for tick := 0; tick < 120 && len(seen) < 6; tick++ {
		base.probeCalls, base.availabilityCalls = nil, nil
		if err := c.Tick(r.now); err != nil {
			t.Fatal(err)
		}
		var id string
		for key, item := range c.state {
			if item.qualityPair != nil {
				id = key
			}
		}
		if len(base.probeCalls) > 1 {
			t.Fatalf("single-slot budget exceeded: %v", base.probeCalls)
		}
		if id == "" {
			r.now = r.now.Add(max(time.Second, c.nextIntervalAt(r.now)))
			continue
		}
		item := c.state[id]
		seen[id] = true
		if !pairContinuationValid(r.now, item) {
			t.Fatalf("other lists expired %s pair before next wake: started=%s finished=%s checks=%v", id, item.qualityPair.StartedAt, r.now, base.availabilityCalls)
		}
		peer := item.qualityPair.Node
		r.now = r.now.Add(c.nextIntervalAt(r.now))
		base.probeCalls = nil
		if err := c.Tick(r.now); err != nil {
			t.Fatal(err)
		}
		if len(base.probeCalls) != 1 || base.probeCalls[0] != peer || !closeQualityPair(item, item.Selected, id+"-reserve") {
			t.Fatalf("pending pair lost its next slot: want=%s calls=%v", peer, base.probeCalls)
		}
		if item.Selected != id+"-active" {
			t.Fatal("a slower reserve changed the route")
		}
		r.now = r.now.Add(max(time.Second, c.nextIntervalAt(r.now)))
	}
	if len(seen) != 6 {
		t.Fatalf("pair scheduling starved some lists: %v", seen)
	}
}

func TestSingleLanePairDoesNotWaitForOtherLivenessRound(t *testing.T) {
	c, base, r := singleLaneSixPolicyFixture(t)
	for id := range base.pool.HealthPolicies {
		if id != "route-0" {
			c.regularNext[id] = r.now.Add(time.Hour)
		}
	}
	if err := c.Tick(r.now); err != nil {
		t.Fatal(err)
	}
	item := c.state["route-0"]
	if !pairContinuationValid(r.now, item) || len(base.availabilityCalls) != 0 {
		t.Fatalf("routine liveness expired the pending pair: now=%s pair=%+v calls=%v", r.now, item.qualityPair, base.availabilityCalls)
	}
	r.now = r.now.Add(c.nextIntervalAt(r.now))
	base.probeCalls = nil
	if err := c.Tick(r.now); err != nil {
		t.Fatal(err)
	}
	if len(base.probeCalls) != 1 || base.probeCalls[0] != "route-0-reserve" || !closeQualityPair(item, item.Selected, "route-0-reserve") {
		t.Fatalf("pair did not finish before slow liveness checks: %v", base.probeCalls)
	}
	if len(base.availabilityCalls) != 5 {
		t.Fatalf("other active paths did not resume after the pair: %v", base.availabilityCalls)
	}
}

func TestSingleLanePairStillYieldsToOtherPendingFailure(t *testing.T) {
	c, _, r := singleLaneSixPolicyFixture(t)
	if err := c.Tick(r.now); err != nil {
		t.Fatal(err)
	}
	item := c.state["route-0"]
	urgent := c.state["route-5"]
	urgent.AvailabilityFailures[urgent.Selected] = 1
	urgent.FailureClass[urgent.Selected] = string(probeFailureTimeout)
	r.events = nil
	r.now = r.now.Add(c.nextIntervalAt(r.now))
	if err := c.Tick(r.now); err != nil {
		t.Fatal(err)
	}
	if len(r.events) == 0 || r.events[0] != "availability:route-5-active" || urgent.AvailabilityFailures[urgent.Selected] != 0 {
		t.Fatalf("pair bypassed the pending-failure proof: %v", r.events)
	}
	if closeQualityPair(item, item.Selected, "route-0-reserve") || item.Selected != "route-0-active" || item.OptimizationChecks != 0 {
		t.Fatal("an urgently delayed pair supplied optimization evidence")
	}
}

func TestBackgroundAvailabilityCancellationJoinsHTTPAndRestoresContext(t *testing.T) {
	original := healthTargets
	defer func() { healthTargets = original }()
	healthTargets = append(healthTargets[:0:0], original[0])
	healthTargets[0].url = "http://probe.invalid/generate_204"
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	primary := newXraySelectorRuntime(Options{ProbeURL: server.URL})
	primary.probeContext = ctx
	primary.selectorMembers["outbound-health-probe"] = "direct-wan"
	done := make(chan struct{})
	runtime := &responsiveSelectorRuntime{selectorRuntime: primary, ctx: ctx, backgroundDone: done}
	go func() {
		select {
		case <-started:
			close(done)
		case <-ctx.Done():
		}
	}()
	background, ok := any(runtime).(interface{ ProbeBackgroundAvailability(string) probeEvidence })
	if !ok {
		t.Fatal("background availability cannot yield to a completed quality probe")
	}
	evidence := background.ProbeBackgroundAvailability("direct-wan")
	if !evidence.Deferred || evidence.OK || evidence.LocalFailure || evidence.Failure != "" || primary.probeContext != ctx || ctx.Err() != nil {
		t.Fatalf("cancellation escaped into routing evidence or damaged parent context: %+v", evidence)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("HTTP request survived cancellation")
	}
}

func TestCompletedQualityDoesNotCancelForcedOrPendingFailure(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprintf("forced=%t", forced), func(t *testing.T) {
			c, base, _ := callbackOrderFixture(t, "best")
			delete(base.pool.HealthPolicies, "z-target")
			delete(c.state, "z-target")
			item := c.state["a-healthy"]
			if forced {
				c.forceLiveness = map[string]bool{"a-healthy": true}
			} else {
				item.AvailabilityFailures["healthy"] = 1
				item.FailureClass["healthy"] = string(probeFailureTimeout)
			}
			done := make(chan struct{})
			primary := &cancellableActiveTestRuntime{base, make(chan struct{}), done, make(chan struct{})}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c.runtime = &responsiveSelectorRuntime{selectorRuntime: primary, ctx: ctx, backgroundDone: done}
			go func() { <-primary.started; close(done) }()
			_ = c.checkDuringProbe(time.Unix(1040, 0), base.pool)
			want := policySettings(base.pool.HealthPolicies["a-healthy"].Policy, "best").failureThreshold
			if forced {
				want = 1
			}
			if item.AvailabilityFailures["healthy"] != want {
				t.Fatalf("urgent proof was deferred: failures=%d want=%d", item.AvailabilityFailures["healthy"], want)
			}
		})
	}
}
