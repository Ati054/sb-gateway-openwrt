package agentport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ati054/xroute-openwrt/internal/xray"
)

type isolatedProbeFixture struct {
	rt               *xraySelectorRuntime
	selected         atomic.Value
	mu               sync.Mutex
	handlers         map[string]bool
	creates, removes int
	rejectAdd        bool
	rejectRemove     bool
}

func newIsolatedProbeFixture(t *testing.T) *isolatedProbeFixture {
	t.Helper()
	f := &isolatedProbeFixture{handlers: map[string]bool{"production-live": true}}
	f.selected.Store("production-live")
	f.rt = newXraySelectorRuntime(Options{XrayBinary: "xray", XrayAPIServer: "127.0.0.1:10085"})
	f.rt.probeSelector = "outbound-health-background-1"
	f.rt.pool = healthPool{Outbounds: map[string]json.RawMessage{"node": json.RawMessage(`{"protocol":"freedom"}`)}}
	f.rt.activeByNode["node"] = "production-live"
	f.rt.loadedDynamic["production-live"] = true
	f.rt.verifiedDynamic["production-live"] = time.Now()
	f.rt.selectorMembers[f.rt.probeSelector] = "production-live"
	f.rt.command = func(ctx context.Context, _ time.Duration, _ string, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch args[1] {
		case "lso":
			tags := []string{}
			for tag := range f.handlers {
				tags = append(tags, tag)
			}
			return outboundTagsJSON(tags...), nil
		case "ado":
			if f.rejectAdd {
				return nil, errors.New("diagnostic add rejected")
			}
			body, e := os.ReadFile(args[len(args)-1])
			if e != nil {
				return nil, e
			}
			var c struct {
				Outbounds []struct {
					Tag string `json:"tag"`
				} `json:"outbounds"`
			}
			if e = json.Unmarshal(body, &c); e != nil {
				return nil, e
			}
			tag := c.Outbounds[0].Tag
			if f.handlers[tag] {
				return nil, errors.New("duplicate diagnostic generation")
			}
			f.handlers[tag] = true
			f.creates++
			return nil, nil
		case "rmo":
			if f.rejectRemove {
				return nil, errors.New("cleanup rejected")
			}
			tag := args[len(args)-1]
			if tag == "production-live" {
				t.Error("removed production handler")
			}
			delete(f.handlers, tag)
			f.removes++
			return nil, nil
		case "bo":
			f.selected.Store(args[len(args)-1])
			return nil, nil
		case "bi":
			return selectorInfo(f.selected.Load().(string)), nil
		default:
			return nil, errors.New("unexpected diagnostic fixture command")
		}
	}
	return f
}

func TestIsolatedPlanningPreservesSlowLiveObservationAndFreshLifecycle(t *testing.T) {
	f := newIsolatedProbeFixture(t)
	old := healthTargets
	healthTargets = append(healthTargets[:0:0], old...)
	healthTargets[0].url = "http://probe.test/204"
	defer func() { healthTargets = old }()
	var live, shadow atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.selected.Load().(string) == "production-live" {
			live.Add(1)
			time.Sleep(45 * time.Millisecond)
		} else {
			shadow.Add(1)
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	f.rt.opts.ProbeURL = server.URL
	for n := 0; n < 2; n++ {
		e := f.rt.ProbeWithQualityRequest("node", qualityProbeRequest{Measurement: qualityMeasurementIsolatedPaired})
		if !e.OK || e.DelayMS == nil || *e.DelayMS < 35 || !validPlanningQuality(e.Planning, qualityMeasurementIsolatedPaired) {
			t.Fatalf("lost live or diagnostic observation: %+v", e)
		}
		if !e.Planning.StartedAt.After(e.ObservedAt) {
			t.Fatal("shadow time overwrote live completion")
		}
		if f.rt.activeByNode["node"] != "production-live" || !f.handlers["production-live"] || f.rt.probeRuntimeTag != "" {
			t.Fatal("diagnostic stage changed production or retained a shadow handler")
		}
	}
	if live.Load() != 2 || shadow.Load() != 2 || f.creates != 2 || f.removes != 2 {
		t.Fatalf("asymmetric lifecycle/cost: live=%d shadow=%d create=%d remove=%d", live.Load(), shadow.Load(), f.creates, f.removes)
	}
}

func TestIsolatedPlanningFailureCannotReplaceHealthyLiveResult(t *testing.T) {
	f := newIsolatedProbeFixture(t)
	f.rejectAdd = true
	old := healthTargets
	healthTargets = append(healthTargets[:0:0], old...)
	healthTargets[0].url = "http://probe.test/204"
	defer func() { healthTargets = old }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	f.rt.opts.ProbeURL = server.URL
	e := f.rt.ProbeWithQualityRequest("node", qualityProbeRequest{Measurement: qualityMeasurementIsolatedPaired})
	if !e.OK || e.DelayMS == nil || validPlanningQuality(e.Planning, qualityMeasurementIsolatedPaired) || !e.Planning.Deferred {
		t.Fatalf("failed shadow became live failure or planning win: %+v", e)
	}
	if !f.handlers["production-live"] {
		t.Fatal("diagnostic error removed live handler")
	}
}

func TestIsolatedPlanningDoesNotHideBrokenLivePath(t *testing.T) {
	f := newIsolatedProbeFixture(t)
	old := healthTargets
	healthTargets = append(healthTargets[:0:0], old...)
	for i := range healthTargets {
		healthTargets[i].url = "http://probe.test/fail"
	}
	defer func() { healthTargets = old }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.selected.Load().(string) == "production-live" {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	f.rt.opts.ProbeURL = server.URL
	e := f.rt.ProbeWithQualityRequest("node", qualityProbeRequest{Measurement: qualityMeasurementIsolatedPaired})
	if e.OK || validPlanningQuality(e.Planning, qualityMeasurementIsolatedPaired) || f.creates != 0 {
		t.Fatalf("shadow bypassed a broken real route: %+v creates=%d", e, f.creates)
	}
}

func TestIsolatedControllerSeparatesQueueFromEndpointDeterioration(t *testing.T) {
	for _, scenario := range []string{"working-queue", "endpoint-slow", "live-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			c, r, base := warmControllerFixture(t, 2, 2, 2)
			c.opts.URLTestPlanningMode = URLTestPlanningIsolatedPaired
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

func TestIsolatedTagRejectsUnsupportedAndNeverReusesOldGeneration(t *testing.T) {
	f := newIsolatedProbeFixture(t)
	f.rt.pool.Outbounds["plugin"] = json.RawMessage(`{"protocol":"shadowsocks","_resproxy_plugin":{"name":"v2ray-plugin"}}`)
	for _, node := range []string{"block", "direct-wan", "absent", "plugin"} {
		if _, e := f.rt.probeTagMode(node, true); !errors.Is(e, errIsolatedProbeUnsupported) {
			t.Fatalf("unsupported %q accepted: %v", node, e)
		}
	}
	first, e := f.rt.probeTagMode("node", true)
	if e != nil {
		t.Fatal(e)
	}
	second, e := f.rt.probeTagMode("node", true)
	if e != nil || first != second || f.creates != 2 || f.removes != 1 {
		t.Fatalf("old transport reused: creates=%d removes=%d err=%v", f.creates, f.removes, e)
	}
	if !f.handlers["production-live"] {
		t.Fatal("replaced real handler")
	}
}

func TestIsolatedCancelledShadowRetainsLiveEvidenceAndJoins(t *testing.T) {
	f := newIsolatedProbeFixture(t)
	old := healthTargets
	healthTargets = append(healthTargets[:0:0], old...)
	healthTargets[0].url = "http://probe.test/204"
	defer func() { healthTargets = old }()
	entered := make(chan struct{})
	left := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.selected.Load().(string) != "production-live" {
			close(entered)
			<-r.Context().Done()
			close(left)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	f.rt.opts.ProbeURL = server.URL
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.rt.probeContext = ctx
	done := make(chan probeEvidence, 1)
	go func() {
		done <- f.rt.ProbeWithQualityRequest("node", qualityProbeRequest{Measurement: qualityMeasurementIsolatedPaired})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("shadow did not start")
	}
	cancel()
	select {
	case e := <-done:
		if !e.OK || !e.Planning.Failed || !f.handlers["production-live"] || f.rt.probeRuntimeTag != "" || len(f.handlers) != 1 {
			t.Fatalf("cancelled shadow damaged live result: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled shadow kept its lane")
	}
	select {
	case <-left:
	case <-time.After(time.Second):
		t.Fatal("shadow HTTP handler not joined")
	}
}

func TestIsolatedSixPoliciesShareOneSlotAndKeepLiveness(t *testing.T) {
	c, r, base := disjointWarmScanFixture(t, 6)
	c.opts.URLTestPlanningMode = URLTestPlanningIsolatedPaired
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

func TestIsolatedPlanningHasSeparateMetricAndKeepsProductionDefault(t *testing.T) {
	if (Options{}).planningMeasurement() == qualityMeasurementIsolatedPaired {
		t.Fatal("experiment became implicit default")
	}
	opts := Options{URLTestPlanningMode: URLTestPlanningIsolatedPaired}
	if opts.validatePlanningMode() != nil || opts.planningMeasurement() != qualityMeasurementIsolatedPaired {
		t.Fatal("explicit experiment not admitted")
	}
	at := time.Now()
	delay := 100
	p := &planningQualityEvidence{Measurement: qualityMeasurementFreshPaired, StartedAt: at, ObservedAt: at.Add(time.Second), DelayMS: &delay}
	if validPlanningQuality(p, qualityMeasurementIsolatedPaired) {
		t.Fatal("old evidence entered new measurement")
	}
	jobs := probeLaneJobsWithMeasurement([]string{"a", "b"}, 2, true, func() {}, qualityMeasurementIsolatedPaired)
	a, b := <-jobs[0], <-jobs[1]
	if a.warmStart == nil || b.warmStart == nil || a.warmStart.barrier != b.warmStart.barrier {
		t.Fatal("shadow peers lack second-stage barrier")
	}
}

func TestIsolatedCleanupFailureRetainsOwnershipAndRetries(t *testing.T) {
	f := newIsolatedProbeFixture(t)
	old := healthTargets
	healthTargets = append(healthTargets[:0:0], old...)
	healthTargets[0].url = "http://probe.test/204"
	defer func() { healthTargets = old }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	f.rt.opts.ProbeURL = server.URL
	f.rejectRemove = true
	e := f.rt.ProbeWithQualityRequest("node", qualityProbeRequest{Measurement: qualityMeasurementIsolatedPaired})
	if !e.OK || !e.Planning.Failed || f.rt.probeRuntimeTag == "" || len(f.handlers) != 2 {
		t.Fatal("unconfirmed cleanup lost ownership")
	}
	f.rejectRemove = false
	e = f.rt.ProbeWithQualityRequest("node", qualityProbeRequest{Measurement: qualityMeasurementIsolatedPaired})
	if !e.OK || !validPlanningQuality(e.Planning, qualityMeasurementIsolatedPaired) || f.rt.probeRuntimeTag != "" || len(f.handlers) != 1 || f.creates != 2 {
		t.Fatal("failed generation was reused or leaked")
	}
}

func TestIsolatedCleanupSerializesWithApplyAndHonorsDeadline(t *testing.T) {
	f := newIsolatedProbeFixture(t)
	if _, err := f.rt.probeTagMode("node", true); err != nil {
		t.Fatal(err)
	}
	xray.MutationMu.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	err := f.rt.retireIsolatedProbe(ctx)
	cancel()
	xray.MutationMu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || f.rt.probeRuntimeTag == "" || f.removes != 0 {
		t.Fatal("cleanup raced Apply or forgot ownership")
	}
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := f.rt.retireIsolatedProbe(ctx); err != nil || f.rt.probeRuntimeTag != "" || f.removes != 1 {
		t.Fatal("retained generation did not retire after Apply")
	}
}

func TestIsolatedAsymmetricPreparationKeepsFullHEADDeadline(t *testing.T) {
	old := healthTargets
	healthTargets = append(healthTargets[:0:0], old...)
	healthTargets[0].url = "http://probe.test/204"
	defer func() { healthTargets = old }()
	fixtures := []*isolatedProbeFixture{newIsolatedProbeFixture(t), newIsolatedProbeFixture(t)}
	for _, f := range fixtures {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if f.selected.Load().(string) != "production-live" {
				select {
				case <-time.After(4300 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
			}
			w.WriteHeader(204)
		}))
		defer server.Close()
		f.rt.opts.ProbeURL = server.URL
	}
	original := fixtures[1].rt.command
	fixtures[1].rt.command = func(ctx context.Context, timeout time.Duration, binary string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[1] == "ado" {
			select {
			case <-time.After(6 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return original(ctx, timeout, binary, args...)
	}
	jobs := probeLaneJobsWithMeasurement([]string{"node", "node"}, 2, true, func() {}, qualityMeasurementIsolatedPaired)
	results := make([]probeEvidence, 2)
	var joined sync.WaitGroup
	for i, f := range fixtures {
		joined.Go(func() {
			job := <-jobs[i]
			results[i] = f.rt.ProbeWithQualityRequest(job.node, qualityProbeRequest{Measurement: qualityMeasurementIsolatedPaired, Start: job.start, WarmStart: job.warmStart})
		})
	}
	joined.Wait()
	for _, e := range results {
		if !e.OK || !validPlanningQuality(e.Planning, qualityMeasurementIsolatedPaired) || *e.Planning.DelayMS < 4200 {
			t.Fatalf("preparation consumed peer HEAD deadline: %+v", e.Planning)
		}
	}
	if gap := results[0].Planning.StartedAt.Sub(results[1].Planning.StartedAt); gap < -100*time.Millisecond || gap > 100*time.Millisecond {
		t.Fatalf("asymmetric preparation broke rendezvous: %v", gap)
	}
}

func TestIsolatedModeRollbackPreservesLiveHistory(t *testing.T) {
	c, _, _ := warmControllerFixture(t, 2, 2, 2)
	item := c.state["route"]
	contract := c.processedPool.HealthPolicies["route"]
	original := item.Samples["node-00"][0]
	c.opts.URLTestPlanningMode = URLTestPlanningIsolatedPaired
	c.prepareWarmPlanning(contract, item)
	item.OptimizationChecks = 1
	c.opts.URLTestPlanningMode = URLTestPlanningFreshPaired
	c.prepareWarmPlanning(contract, item)
	if item.Selected != "node-00" || len(item.Samples["node-00"]) != 1 || item.Samples["node-00"][0].At != original.At || item.OptimizationChecks != 0 || item.WarmPlanningMarker != string(qualityMeasurementFreshPaired) {
		t.Fatal("rollback mixed planning evidence or erased live selection/history")
	}
}
