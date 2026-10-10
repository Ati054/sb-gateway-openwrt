package agentport

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeCPUCounterEvidence(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stat")
	if err := os.WriteFile(p, []byte("cpu 100 10 20 200 30 5 6 7 99 9\ncpu0 1 2 3 4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, ok := readProbeCPU(p)
	if !ok || c.total != 378 || c.idle != 230 {
		t.Fatalf("guest counters counted twice: %+v %v", c, ok)
	}
	if _, ok := readProbeCPU(p + "missing"); ok {
		t.Fatal("missing proc counters reported as evidence")
	}
	for _, tc := range []struct {
		before, after cpuCounters
		busy          bool
	}{
		{cpuCounters{100, 50}, cpuCounters{200, 55}, true},
		{cpuCounters{100, 50}, cpuCounters{200, 56}, false},
		{cpuCounters{100, 50}, cpuCounters{100, 50}, false},
		{cpuCounters{100, 50}, cpuCounters{90, 40}, false},
	} {
		if got := probeCPUSaturated(tc.before, tc.after); got != tc.busy {
			t.Fatalf("invalid busy evidence: %+v got=%v", tc, got)
		}
	}
}
func TestSaturatedCPUTimeoutStillProbesReservesBeforeBlocking(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		for _, reserveOK := range []bool{true, false} {
			t.Run(mode+map[bool]string{true: "/working-reserve", false: "/no-proof"}[reserveOK], func(t *testing.T) {
				controller, runtime, item := livenessFixture(t, mode)
				runtime.probes["de"] = probeEvidence{Failure: probeFailureTimeout, CPUOverloaded: true}
				runtime.probes["nl"] = probeEvidence{Failure: probeFailureTimeout, CPUOverloaded: true}
				if reserveOK {
					runtime.probes["nl"] = successfulEvidence(300)
				}
				runtime.availabilityCalls = nil
				for _, at := range []int64{1058, 1060} {
					if err := controller.Tick(time.Unix(at, 0)); err != nil {
						t.Fatal(err)
					}
				}
				want := "de"
				if reserveOK {
					want = "nl"
				}
				if item.Selected != want || !contains(runtime.availabilityCalls, "nl") {
					t.Fatalf("CPU saturation prevented reserve discovery or blocked without remote proof: selected=%s probes=%v", item.Selected, runtime.availabilityCalls)
				}
				if !reserveOK {
					if item.AvailabilityFailures["de"] != 0 || hasSelection(runtime.selections, "europe", "block") {
						t.Fatal("overload retained withdrawal/failure counter")
					}
					// Low-load independent failures must still fail closed.
					runtime.probes["de"] = probeEvidence{Failure: probeFailureTimeout}
					runtime.probes["nl"] = probeEvidence{Failure: probeFailureTimeout}
					controller.forceLiveness = map[string]bool{"europe": true}
					for _, at := range []int64{1065, 1067} {
						if err := controller.Tick(time.Unix(at, 0)); err != nil {
							t.Fatal(err)
						}
					}
					if item.Selected != "block" {
						t.Fatalf("known low-load outage did not fail closed: %s", item.Selected)
					}
				}
			})
		}
	}
}

func TestCPUUnknownTimeoutDoesNotBecomeRemoteHistory(t *testing.T) {
	item := newPolicyHealthState()
	updateHistories(item, []string{"node"}, map[string]probeEvidence{"node": successfulEvidence(100)}, time.Unix(1000, 0))
	updateHistories(item, []string{"node"}, map[string]probeEvidence{"node": {Failure: probeFailureTimeout, CPUOverloaded: true}}, time.Unix(1005, 0))
	if len(item.DailySamples["node"]) != 1 {
		t.Fatal("local saturation was charged as remote failure")
	}
	updateHistories(item, []string{"node"}, map[string]probeEvidence{"node": {Failure: probeFailureTimeout}}, time.Unix(1010, 0))
	if len(item.DailySamples["node"]) != 2 || item.DailySamples["node"][1].OK {
		t.Fatal("ordinary timeout disappeared from history")
	}
}
