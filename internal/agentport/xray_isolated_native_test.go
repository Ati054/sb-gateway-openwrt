package agentport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// Opt-in controlled-peer integration. The fixture supplies synthetic identities,
// a pinned local origin and two private API/probe lanes; no field secrets.
func TestIsolatedNativeControlledHandlers(t *testing.T) {
	path := os.Getenv("SB_ISOLATED_NATIVE_CONFIG")
	if path == "" {
		t.Skip("controlled network fixture opt-in")
	}
	body, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var definition json.RawMessage
	if json.Unmarshal(body, &definition) != nil {
		t.Fatal("invalid fixture definition")
	}
	old := healthTargets
	healthTargets = append(healthTargets[:0:0], old...)
	healthTargets[0].url = "https://10.97.13.2:24443/204"
	defer func() { healthTargets = old }()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	control, err := newXrayControlClient("127.0.0.1:19190")
	if err != nil {
		t.Fatal(err)
	}
	defer control.close()
	nodes := []string{"node-A", "node-B"}
	liveMember, err := control.selected(ctx, "lab-route")
	if err != nil {
		t.Fatal(err)
	}
	liveNode := "node-A"
	if liveMember == "live-B" {
		liveNode = "node-B"
	} else if liveMember != "live-A" {
		t.Fatal("unknown controlled live selection")
	}
	scope := policyProbeScope{Policy: "lab-route", Selected: liveNode, Member: liveMember}
	runtimes := make([]*xraySelectorRuntime, 2)
	for i, node := range nodes {
		r := newXraySelectorRuntime(Options{XrayBinary: os.Getenv("SB_ISOLATED_XRAY"), XrayAPIServer: "127.0.0.1:19190", ProbeURL: fmt.Sprintf("http://127.0.0.1:%d", 19110+i)})
		r.probeSelector = fmt.Sprintf("isolated-probe-%d", i)
		r.probeContext = ctx
		r.control = control
		tag := "live-A"
		if i == 1 {
			tag = "live-B"
		}
		r.pool = healthPool{Outbounds: map[string]json.RawMessage{node: definition}}
		r.activeByNode[node] = tag
		r.loadedDynamic[tag] = true
		r.verifiedDynamic[tag] = time.Now()
		runtimes[i] = r
	}
	for round := 0; round < 8; round++ {
		jobs := probeLaneJobsWithMeasurement(nodes, 2, true, cancel, qualityMeasurementRoutePaired)
		results := make([]probeEvidence, 2)
		var joined sync.WaitGroup
		for i, r := range runtimes {
			joined.Go(func() {
				job := <-jobs[i]
				results[i] = r.ProbeWithQualityRequest(job.node, qualityProbeRequest{policyProbeScope: scope, Measurement: qualityMeasurementRoutePaired, Start: job.start, WarmStart: job.warmStart})
			})
		}
		joined.Wait()
		for i, result := range results {
			if !result.OK || !validPlanningQuality(result.Planning, qualityMeasurementRoutePaired) {
				t.Fatalf("real live/shadow request failed in lane %d: live-ok=%v shadow=%+v", i, result.OK, result.Planning)
			}
			if runtimes[i].probeRuntimeTag != "" {
				t.Fatal("native shadow handler not reclaimed")
			}
		}
		a, b := results[0].Planning, results[1].Planning
		gap := a.StartedAt.Sub(b.StartedAt)
		if gap < 0 {
			gap = -gap
		}
		if gap > 100*time.Millisecond {
			t.Fatalf("isolated phases not aligned: %v", gap)
		}
		v := map[string]any{"round": round, "live_a_ms": results[0].DelayMS, "live_b_ms": results[1].DelayMS, "planning_a_ms": a.DelayMS, "planning_b_ms": b.DelayMS, "shadow_start_gap_ms": gap.Milliseconds(), "measurement": string(a.Measurement)}
		raw, _ := json.Marshal(v)
		fmt.Printf("ISOLATED_NATIVE_RECORD %s\n", raw)
		time.Sleep(500 * time.Millisecond)
	}

	// Exercise real HandlerService removal after the request context is gone.
	r := runtimes[0]
	if err := r.selectProbeMode(r.probeSelectorName(), nodes[0], true); err != nil {
		t.Fatal(err)
	}
	abandoned, stop := context.WithCancel(ctx)
	stop()
	r.probeContext = abandoned
	cleanup, done := context.WithTimeout(context.WithoutCancel(abandoned), 2*time.Second)
	err = r.retireIsolatedProbe(cleanup)
	done()
	r.probeContext = ctx
	if err != nil || r.probeRuntimeTag != "" {
		t.Fatal("real cancelled probe cleanup was not confirmed")
	}
	for i, r := range runtimes {
		tag := "live-A"
		if i == 1 {
			tag = "live-B"
		}
		if present, e := r.outboundPresent(tag); e != nil || !present {
			t.Fatal("production handler damaged by diagnostic lifecycle")
		}
	}
}
