package agentport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

func scopedProbeFixture(t *testing.T) (*isolatedProbeFixture, map[string]string) {
	t.Helper()
	f := newIsolatedProbeFixture(t)
	selectors := map[string]string{"P": f.rt.policyRuntimeTag("P", "node"), "Q": f.rt.policyRuntimeTag("Q", "node")}
	for _, tag := range selectors {
		f.handlers[tag] = true
	}
	f.rt.activeByNode["node"] = selectors["Q"] // Deliberately wrong for P.
	previous := f.rt.command
	f.rt.command = func(ctx context.Context, limit time.Duration, binary string, args ...string) ([]byte, error) {
		switch args[1] {
		case "bo":
			f.mu.Lock()
			selectors[args[len(args)-2]] = args[len(args)-1]
			f.mu.Unlock()
			f.selected.Store(args[len(args)-1])
			return nil, nil
		case "bi":
			f.mu.Lock()
			tag := selectors[args[len(args)-1]]
			f.mu.Unlock()
			return selectorInfo(tag), nil
		}
		return previous(ctx, limit, binary, args...)
	}
	return f, selectors
}
func TestPolicyScopedProbeUsesExactLiveHandler(t *testing.T) {
	for _, order := range [][]string{{"P", "Q"}, {"Q", "P"}} {
		f, selectors := scopedProbeFixture(t)
		for _, policy := range order {
			result := f.rt.policyProbe(policyProbeScope{Policy: policy}, nil, func(r *xraySelectorRuntime) probeEvidence {
				tag, err := r.probeTag("node")
				if err != nil || tag != selectors[policy] {
					t.Fatalf("%s borrowed handler: %s %v", policy, tag, err)
				}
				return successfulEvidence(100)
			})
			if !result.OK {
				t.Fatalf("scoped request failed: %+v", result)
			}
		}
		if f.creates != 0 || f.removes != 0 {
			t.Fatal("live probe mutated production inventory")
		}
	}
}
func TestPolicyScopedProbeNeverRepairsMissingLiveWithAnotherPolicy(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	delete(f.handlers, selectors["P"])
	result := f.rt.policyProbe(policyProbeScope{Policy: "P"}, nil, func(r *xraySelectorRuntime) probeEvidence {
		_, err := r.probeTag("node")
		if err == nil {
			t.Fatal("missing P masked by healthy Q or fresh clone")
		}
		return probeEvidence{LocalFailure: true, LocalError: err}
	})
	if !result.LocalFailure || f.creates != 0 {
		t.Fatal("missing live handler was silently repaired")
	}
}
func TestPolicyScopedReserveDoesNotBorrowAnotherPolicy(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	selectors["P"] = "block"
	result := f.rt.policyProbe(policyProbeScope{Policy: "P"}, nil, func(r *xraySelectorRuntime) probeEvidence {
		tag, err := r.probeTag("node")
		if err != nil || tag == selectors["Q"] || tag == "production-live" {
			t.Fatalf("reserve borrowed another policy: %s %v", tag, err)
		}
		return successfulEvidence(100)
	})
	if !result.OK || f.creates != 1 {
		t.Fatal("reserve not independently checked")
	}
}
func TestPolicyScopedProbeDiscardsSelectionChangeDuringHTTP(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	result := f.rt.policyProbe(policyProbeScope{Policy: "P"}, nil, func(r *xraySelectorRuntime) probeEvidence {
		selectors["P"] = "block"
		return successfulEvidence(1)
	})
	if !result.Deferred || result.OK || result.LocalFailure {
		t.Fatalf("stale evidence accepted: %+v", result)
	}
}
func TestRoutePlanningMeasuresSharedHandlerTwiceWithoutRetiringIt(t *testing.T) {
	f := newIsolatedProbeFixture(t)
	f.rt.pool.Outbounds["node"] = json.RawMessage(`{"protocol":"shadowsocks","_resproxy_plugin":{}}`)
	old := healthTargets
	healthTargets = append(healthTargets[:0:0], old...)
	defer func() { healthTargets = old }()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	healthTargets[0].url = "http://probe.test/204"
	f.rt.opts.ProbeURL = server.URL
	e := f.rt.ProbeWithQualityRequest("node", qualityProbeRequest{Measurement: qualityMeasurementRoutePaired})
	if !e.OK || !validPlanningQuality(e.Planning, qualityMeasurementRoutePaired) || e.Planning.Transport != "shared-session" || calls.Load() != 2 {
		t.Fatalf("not two fresh route requests: calls=%d %+v", calls.Load(), e)
	}
	if f.creates != 0 || f.removes != 0 || !f.handlers["production-live"] {
		t.Fatal("shared handler was removed or cloned")
	}
}
func TestRoutePlanningCapabilities(t *testing.T) {
	for _, test := range []struct{ definition, want string }{
		{`{"protocol":"vless"}`, "independent-handler"},
		{`{"protocol":"shadowsocks","_resproxy_plugin":{}}`, "shared-session"},
		{`{"protocol":"vless","proxySettings":{"tag":"detour"}}`, "shared-session"},
		{`{"protocol":"vless","streamSettings":{"sockopt":{"dialerProxy":"detour"}}}`, "shared-session"},
		{`{"protocol":"wireguard"}`, "shared-session"},
	} {
		f := newIsolatedProbeFixture(t)
		f.rt.pool.Outbounds["node"] = json.RawMessage(test.definition)
		got, err := f.rt.planningTransport("node")
		if err != nil || got != test.want {
			t.Fatalf("%s got %s %v", test.definition, got, err)
		}
	}
	f := newIsolatedProbeFixture(t)
	if got, e := f.rt.planningTransport("direct-wan"); e != nil || got != "direct" {
		t.Fatal(got, e)
	}
	if _, e := f.rt.planningTransport("block"); !errors.Is(e, errIsolatedProbeUnsupported) {
		t.Fatal("block accepted")
	}
}
func TestRoutePlanningDoesNotFallBackForMultiplePolicies(t *testing.T) {
	c, r, _ := warmControllerFixture(t, 2, 2, 2)
	c.opts.URLTestPlanningMode = URLTestPlanningRoutePaired
	c.processedPool = r.pool
	for _, id := range []string{"one", "two", "three", "four", "five", "six"} {
		c.processedPool.HealthPolicies[id] = healthPolicyContract{Mode: "best", Candidates: []string{"node-00", "node-01"}}
		c.warmStarted[id] = true
		if got := c.qualityMeasurementForPolicy(id, c.processedPool.HealthPolicies[id]); got != qualityMeasurementRoutePaired {
			t.Fatalf("%s fell back: %s", id, got)
		}
	}
}

type policyScopedTestRuntime struct{ *fakeSelectorRuntime }

func (r *policyScopedTestRuntime) ProbeAvailabilityWithRequest(node string, request availabilityProbeRequest) probeEvidence {
	return r.ProbeAvailability(node)
}
func TestPolicyScopedProbeCacheDoesNotShareLiveEvidence(t *testing.T) {
	r := &policyScopedTestRuntime{&fakeSelectorRuntime{probes: map[string]probeEvidence{"a": successfulEvidence(100)}}}
	c := &healthController{runtime: r}
	c.beginProbeBudget(healthPool{ProbeBudget: 2}, []string{"P", "Q"})
	contract := budgetContract("a")
	c.sharedQualityProbe("P", "a", contract)
	c.sharedQualityProbe("Q", "a", contract)
	if len(r.probeCalls) != 2 || c.probeKey("P", "a", contract) == c.probeKey("Q", "a", contract) {
		t.Fatal("composite live evidence crossed policies")
	}
}

func TestRouteSixPoliciesShareOneSlotAndKeepLiveness(t *testing.T) {
	c, r, base := disjointWarmScanFixture(t, 6)
	c.opts.URLTestPlanningMode = URLTestPlanningRoutePaired
	r.advanceClock = true
	for _, contract := range r.pool.HealthPolicies {
		a, b := contract.Candidates[0], contract.Candidates[1]
		r.probes[a], r.probes[b] = successfulEvidence(900), successfulEvidence(100)
		r.warm[a], r.warm[b] = 900, 100
	}
	now := base
	complete := false
	activeEnd := map[string]time.Time{}
	for n := 0; n < 800 && now.Before(base.Add(600*time.Second)); n++ {
		r.at = now
		before, heads := len(r.probeCalls), r.heads
		if err := c.Tick(now); err != nil {
			t.Fatal(err)
		}
		if len(r.probeCalls)-before > 1 || r.heads-heads > 2 {
			t.Fatal("single quality slot or two-stage HTTP cost exceeded")
		}
		complete = true
		for id, item := range c.state {
			if item.Selected != r.pool.HealthPolicies[id].Candidates[1] {
				complete = false
			} else if node := r.pool.HealthPolicies[id].Candidates[0]; activeEnd[node].IsZero() {
				activeEnd[node] = r.at
			}
		}
		if complete {
			break
		}
		now = r.at.Add(c.nextIntervalAt(r.at))
	}
	if !complete {
		t.Fatal("one-slot isolated scan starved policies")
	}
	maxGap := time.Duration(0)
	for node, end := range activeEnd {
		times := append([]time.Time{base}, r.availabilityAt[node]...)
		times = append(times, r.workingProofAt[node]...)
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		for i := 1; i < len(times); i++ {
			if times[i].After(end) {
				break
			}
			if gap := times[i].Sub(times[i-1]); gap > maxGap {
				maxGap = gap
			}
		}
	}
	if maxGap > 10*time.Second {
		t.Fatalf("shadow delayed active availability by %v", maxGap)
	}
	t.Logf("six-policy single-slot model: convergence=%v max-liveness-gap=%v heads=%d", now.Sub(base), maxGap, r.heads)
}

func TestPolicyScopedQualityKeepsBadAndHealthyLivePathsSeparate(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	old := healthTargets
	healthTargets = append(healthTargets[:0:0], old...)
	defer func() { healthTargets = old }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.selected.Load().(string) == selectors["P"] {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	for i := range healthTargets {
		healthTargets[i].url = "http://probe.test/204"
		healthTargets[i].status = 204
	}
	f.rt.opts.ProbeURL = server.URL
	bad := f.rt.ProbeWithQualityRequest("node", qualityProbeRequest{policyProbeScope: policyProbeScope{Policy: "P"}, Measurement: qualityMeasurementRoutePaired})
	good := f.rt.ProbeWithQualityRequest("node", qualityProbeRequest{policyProbeScope: policyProbeScope{Policy: "Q"}, Measurement: qualityMeasurementRoutePaired})
	if bad.OK || !good.OK || !validPlanningQuality(good.Planning, qualityMeasurementRoutePaired) {
		t.Fatalf("bad=%+v good=%+v", bad, good)
	}
	if f.rt.probePolicy != "" {
		t.Fatal("per-request scope leaked into runtime")
	}
	if selectors["P"] != f.rt.policyRuntimeTag("P", "node") || selectors["Q"] != f.rt.policyRuntimeTag("Q", "node") {
		t.Fatal("diagnostics changed production selectors")
	}
}

type scopedWarmModel struct{ *warmPlanningTestRuntime }

func (r *scopedWarmModel) ProbeAvailabilityWithRequest(node string, req availabilityProbeRequest) probeEvidence {
	return r.ProbeAvailability(node)
}
func TestRouteSixOverlappingPoliciesShareOneSlot(t *testing.T) {
	c, r, base := warmControllerFixture(t, 2, 1, 1)
	c.opts.URLTestPlanningMode = URLTestPlanningRoutePaired
	c.runtime = &scopedWarmModel{r}
	contract := r.pool.HealthPolicies["route"]
	for _, id := range []string{"P", "Q", "R", "S", "T", "U"} {
		r.pool.HealthPolicies[id] = contract
		item := *c.state["route"]
		// Allocate independent state; a shared endpoint never shares confirmations.
		item = *newPolicyHealthState()
		ensureHealthMaps(&item)
		item.AvailabilityOK, item.QualityOK = map[string]bool{}, map[string]bool{}
		item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "node-00", "node-00", true
		item.Mode, item.LatencyMeasurement, item.CandidateSignature = "best", "gstatic-head-v1", "node-00\nnode-01"
		item.CandidateNodes = contract.Nodes
		for _, node := range contract.Candidates {
			delay := 900
			if node == "node-01" {
				delay = 100
			}
			item.Samples[node] = []healthSample{{At: 998, OK: true, DelayMS: &delay}}
			item.LastProbeAt[node], item.Recoveries[node] = 935, 3
			item.AvailabilityOK[node], item.QualityOK[node] = true, true
		}
		c.state[id] = &item
		c.warmStarted[id] = true
		r.current[id] = "node-00"
	}
	delete(c.state, "route")
	delete(r.pool.HealthPolicies, "route")
	delete(r.current, "route")
	delete(c.warmStarted, "route")
	r.probes["node-00"], r.probes["node-01"] = successfulEvidence(900), successfulEvidence(100)
	r.warm["node-00"], r.warm["node-01"] = 900, 100
	r.advanceClock = true
	complete := false
	for n := 0; n < 1000 && r.at.Before(base.Add(600*time.Second)); n++ {
		before := len(r.probeCalls)
		if e := c.Tick(r.at); e != nil {
			t.Fatal(e)
		}
		if len(r.probeCalls)-before > 1 {
			t.Fatal("global slot budget exceeded")
		}
		complete = true
		for _, item := range c.state {
			if item.Selected != "node-01" {
				complete = false
			}
		}
		if complete {
			break
		}
		r.at = r.at.Add(c.nextIntervalAt(r.at))
	}
	if !complete {
		t.Fatal("overlapping policies starved under scoped evidence")
	}
}
func TestRouteTransportCapabilityChangeInvalidatesConfirmation(t *testing.T) {
	c, r, base := warmControllerFixture(t, 2, 2, 2)
	c.opts.URLTestPlanningMode = URLTestPlanningRoutePaired
	item := c.state["route"]
	c.prepareWarmPlanning(r.pool.HealthPolicies["route"], item)
	now := base.Add(time.Second)
	d := 100
	first := probeEvidence{OK: true, ObservedAt: now, Planning: &planningQualityEvidence{Measurement: qualityMeasurementRoutePaired, Transport: "independent-handler", StartedAt: base, ObservedAt: now, DelayMS: &d}}
	p := policySettings(r.pool.HealthPolicies["route"].Policy, "best")
	collectWarmPlanning(now, []string{"node-01"}, map[string]probeEvidence{"node-01": first}, item, p)
	item.OptimizationCandidate = "node-01"
	item.OptimizationChecks = 1
	next := first
	v := *first.Planning
	v.Transport = "shared-session"
	v.StartedAt = now
	v.ObservedAt = now.Add(time.Second)
	next.Planning = &v
	next.ObservedAt = v.ObservedAt
	collectWarmPlanning(v.ObservedAt, []string{"node-01"}, map[string]probeEvidence{"node-01": next}, item, p)
	if item.OptimizationChecks != 0 || item.OptimizationCandidate != "" || len(item.WarmPlanningSamples["node-01"]) != 1 {
		t.Fatal("capability change inherited old confirmations")
	}
}

func TestPolicyScopedReadbackFailureReleasesBothPairedBarriers(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	previous := f.rt.command
	f.rt.command = func(ctx context.Context, limit time.Duration, binary string, args ...string) ([]byte, error) {
		if args[1] == "bi" && args[len(args)-1] == "P" {
			return nil, errors.New("selector RPC unavailable")
		}
		return previous(ctx, limit, binary, args...)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	jobs := probeLaneJobsWithMeasurement([]string{"node", "node"}, 2, true, cancel, qualityMeasurementRoutePaired)
	a, b := <-jobs[0], <-jobs[1]
	started := time.Now()
	result := f.rt.ProbeWithQualityRequest("node", qualityProbeRequest{policyProbeScope: policyProbeScope{Policy: "P"}, Measurement: qualityMeasurementRoutePaired, Start: a.start, WarmStart: a.warmStart})
	if !result.LocalFailure {
		t.Fatal("readback failure disappeared")
	}
	if err := b.start.wait(ctx); err != nil {
		t.Fatalf("first barrier leaked: %v", err)
	}
	if err := b.warmStart.waitWarm(ctx); !errors.Is(err, errWarmPeerFailed) {
		t.Fatalf("second barrier not aborted: %v", err)
	}
	if time.Since(started) > 200*time.Millisecond || f.creates != 0 || selectors["Q"] == "" {
		t.Fatal("failed scope left peer blocked")
	}
}

func TestPolicyScopedOldSelectedGenerationIsNeverReplacedByHealthyClone(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	old := "old-selected-generation"
	f.handlers[old] = true
	selectors["P"] = old
	scope := policyProbeScope{Policy: "P", Selected: "node", Member: old}
	result := f.rt.policyProbe(scope, nil, func(r *xraySelectorRuntime) probeEvidence {
		tag, e := r.probeTag("node")
		if e != nil || tag != old {
			t.Fatalf("old selected handler replaced: %s %v", tag, e)
		}
		return probeEvidence{Failure: probeFailureTimeout}
	})
	if result.OK || f.creates != 0 {
		t.Fatal("healthy new definition masked old live path")
	}
	selectors["P"] = selectors["Q"]
	result = f.rt.policyProbe(scope, nil, func(r *xraySelectorRuntime) probeEvidence {
		t.Fatal("stale captured scope dispatched")
		return successfulEvidence(1)
	})
	if !result.Deferred {
		t.Fatal("changed member accepted before dispatch")
	}
}
func TestPolicyScopedAvailabilityCancellationIncludesInitialReadback(t *testing.T) {
	f, _ := scopedProbeFixture(t)
	f.rt.command = func(ctx context.Context, _ time.Duration, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := f.rt.ProbeAvailabilityWithRequest("node", availabilityProbeRequest{policyProbeScope: policyProbeScope{Policy: "P"}, Context: ctx})
	if !result.Deferred || result.LocalFailure || time.Since(started) > 300*time.Millisecond {
		t.Fatalf("initial readback escaped cancellation: %+v", result)
	}
}
func TestPolicyScopedCacheIncludesAliasAndSelectionIdentity(t *testing.T) {
	r := &policyScopedTestRuntime{&fakeSelectorRuntime{}}
	c := &healthController{runtime: r, state: healthState{"P": {Selected: "a", RuntimeSelected: "a", RuntimeConfirmed: true}}}
	contract := budgetContract("a", "b")
	contract.Nodes["b"] = contract.Nodes["a"]
	a := c.probeKey("P", "a", contract)
	b := c.probeKey("P", "b", contract)
	if a == b {
		t.Fatal("aliased live and diagnostic paths share composite evidence")
	}
	c.state["P"].Selected, c.state["P"].RuntimeSelected = "b", "b"
	if a == c.probeKey("P", "a", contract) {
		t.Fatal("cached composite evidence survived selected-role change")
	}
}

func TestRouteControllerSeparatesQueueFromEndpointDeterioration(t *testing.T) {
	for _, scenario := range []string{"working-queue", "endpoint-slow", "live-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			c, r, base := warmControllerFixture(t, 2, 2, 2)
			c.opts.URLTestPlanningMode = URLTestPlanningRoutePaired
			c.opts.HealthInterval = time.Second
			r.advanceClock = true
			r.probes["node-00"], r.probes["node-01"] = successfulEvidence(1400), successfulEvidence(400)
			r.warm["node-00"], r.warm["node-01"] = 400, 400
			if scenario == "endpoint-slow" {
				r.warm["node-00"] = 1400
			}
			if scenario == "live-unavailable" {
				r.probes["node-00"] = failedEvidence()
			}
			now := base
			for n := 0; n < 200 && now.Before(base.Add(190*time.Second)); n++ {
				r.at = now
				if err := c.Tick(now); err != nil {
					t.Fatal(err)
				}
				if scenario == "working-queue" && c.state["route"].Selected != "node-00" {
					t.Fatal("moved to an unloaded copy without endpoint improvement")
				}
				if scenario == "endpoint-slow" && now.Before(base.Add(60*time.Second)) && c.state["route"].Selected != "node-00" {
					t.Fatal("endpoint change skipped independent confirmations")
				}
				if scenario != "working-queue" && c.state["route"].Selected == "node-01" {
					return
				}
				now = r.at.Add(c.nextIntervalAt(r.at))
			}
			if scenario != "working-queue" {
				t.Fatalf("required switch was suppressed for %s", scenario)
			}
			live := c.state["route"].MedianDelayMS["node-00"]
			if live == nil || *live != 1400 {
				t.Fatalf("slow working observation hidden from ordinary history: %v", live)
			}
		})
	}
}

func TestPolicyScopedConcurrentQualityAndAvailabilityKeepTheirOwnHandlers(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	old := healthTargets
	healthTargets = append(healthTargets[:0:0], old...)
	defer func() { healthTargets = old }()
	for i := range healthTargets {
		healthTargets[i].url = "http://probe.test/204"
		healthTargets[i].status = 204
	}
	lane := newXraySelectorRuntime(f.rt.opts)
	lane.pool = f.rt.pool
	lane.command = f.rt.command
	lane.probeSelector = "background-quality-Q"
	entered := make(chan struct{})
	var signaled atomic.Bool
	makeProxy := func(selector string, delay bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			tag := selectors[selector]
			bad := tag == selectors["P"]
			f.mu.Unlock()
			if delay {
				if signaled.CompareAndSwap(false, true) {
					close(entered)
				}
				time.Sleep(50 * time.Millisecond)
			}
			if bad {
				w.WriteHeader(503)
			} else {
				w.WriteHeader(204)
			}
		}))
	}
	front, back := makeProxy(f.rt.probeSelector, false), makeProxy(lane.probeSelector, true)
	defer front.Close()
	defer back.Close()
	f.rt.opts.ProbeURL, lane.opts.ProbeURL = front.URL, back.URL
	q := policyProbeScope{Policy: "Q", Selected: "node", Member: selectors["Q"]}
	p := policyProbeScope{Policy: "P", Selected: "node", Member: selectors["P"]}
	done := make(chan probeEvidence, 1)
	go func() {
		done <- lane.ProbeWithQualityRequest("node", qualityProbeRequest{policyProbeScope: q, Measurement: qualityMeasurementRoutePaired})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Q did not start")
	}
	bad := f.rt.ProbeAvailabilityWithRequest("node", availabilityProbeRequest{policyProbeScope: p})
	good := <-done
	if bad.OK || !good.OK || !validPlanningQuality(good.Planning, qualityMeasurementRoutePaired) {
		t.Fatalf("P/Q crossed: P=%+v Q=%+v", bad, good)
	}
}

func TestPolicyScopedCancellationDuringFinalReadbackDefersEvidence(t *testing.T) {
	f, selectors := scopedProbeFixture(t)
	previous := f.rt.command
	reads := 0
	f.rt.command = func(ctx context.Context, limit time.Duration, binary string, args ...string) ([]byte, error) {
		if args[1] == "bi" && args[len(args)-1] == "P" {
			reads++
			if reads == 2 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
		}
		return previous(ctx, limit, binary, args...)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	scope := policyProbeScope{Policy: "P", Selected: "node", Member: selectors["P"]}
	e := f.rt.policyProbe(scope, ctx, func(*xraySelectorRuntime) probeEvidence { return successfulEvidence(1) })
	if !e.Deferred || e.LocalFailure || e.OK {
		t.Fatalf("final cancelled readback became failure or proof: %+v", e)
	}
}
