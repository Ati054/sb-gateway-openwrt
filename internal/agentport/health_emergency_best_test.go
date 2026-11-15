package agentport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func emergencyBestState() *policyHealthState {
	item := newPolicyHealthState()
	ensureHealthMaps(item)
	item.Selected = "active"
	item.AvailabilityOK, item.QualityOK = map[string]bool{}, map[string]bool{}
	item.MedianDelayMS = map[string]*int{}
	item.DailyStats = map[string]healthStats{}
	for i, id := range []string{"a", "b"} {
		delay := 100 + i*100
		item.Samples[id] = []healthSample{{At: 990, OK: true, DelayMS: &delay}}
		item.AvailabilityOK[id], item.QualityOK[id], item.Recoveries[id] = true, true, 3
	}
	return item
}

func TestEmergencyBestWaitsForFasterQualifiedReserve(t *testing.T) {
	for _, failure := range []bool{false, true} {
		preferred := successfulEvidence(500) // Availability timing is not quality.
		want := "a"
		if failure {
			preferred, want = failedEvidence(), "b"
		}
		base := &fakeSelectorRuntime{probes: map[string]probeEvidence{
			"a": preferred, "b": successfulEvidence(5),
		}}
		controller := &healthController{runtime: &reverseParallelRuntime{base}, state: healthState{"route": emergencyBestState()}}
		measured, selected, err := controller.probeEmergencyCandidates(time.Unix(1000, 0), "route", []string{"b", "a"}, effectivePolicySettings{mode: "best", backup: 120, recoveryThreshold: 3})
		if err != nil || selected != want || len(measured) != 2 || len(base.selections) != 1 || base.selections[0][1] != want {
			t.Fatalf("availability completion order stole quality preference: selected=%s switches=%v measured=%v err=%v", selected, base.selections, measured, err)
		}
	}
}

func TestEmergencyBestDoesNotHideLocalFailureBehindCompletedSuccess(t *testing.T) {
	base := &fakeSelectorRuntime{probes: map[string]probeEvidence{
		"a": {LocalFailure: true}, "b": successfulEvidence(5),
	}}
	controller := &healthController{runtime: &reverseParallelRuntime{base}, state: healthState{"route": emergencyBestState()}}
	_, selected, err := controller.probeEmergencyCandidates(time.Unix(1000, 0), "route", []string{"b", "a"}, effectivePolicySettings{mode: "best", backup: 120, recoveryThreshold: 3})
	if selected != "" || len(base.selections) != 0 || !errors.Is(err, errProbeSelectorUnavailable) {
		t.Fatalf("local failure became a node switch: selected=%s err=%v", selected, err)
	}
}

func TestEmergencyPreferenceUsesTimestampedQualityNotHistoryOrLastAvailability(t *testing.T) {
	p := effectivePolicySettings{mode: "best", backup: 120, recoveryThreshold: 3}
	for _, tc := range []struct {
		name      string
		at        float64
		ok        bool
		delay     *int
		qualified bool
	}{
		{"fresh", 990, true, emergencyDelay(100), true},
		{"boundary", 880, true, emergencyDelay(100), true},
		{"expired quality recent availability", 879, true, emergencyDelay(100), false},
		{"future", 1001, true, emergencyDelay(100), false},
		{"failed", 990, false, emergencyDelay(100), false},
		{"availability only", 990, true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := emergencyBestState()
			item.Samples["a"] = []healthSample{{At: tc.at, OK: tc.ok, DelayMS: tc.delay}}
			item.LastProbeAt["a"] = 1000
			item.MedianDelayMS["a"], item.MedianDelayMS["b"] = emergencyDelay(900), emergencyDelay(1)
			item.DailyStats["b"] = healthStats{MedianMS: emergencyDelay(1)}
			want := "b"
			if tc.qualified {
				want = "a"
			}
			if got := knownFreshReserve(time.Unix(1000, 0), "active", []string{"b", "a"}, "best", nil, item, p); got != want {
				t.Fatalf("fresh reserve=%s want=%s", got, want)
			}
		})
	}
}

func TestEmergencyBestPreferredTimeoutReleasesCompletedReserve(t *testing.T) {
	originalPrimary, originalFallback := availabilityProbeTimeout, availabilityFallbackTimeout
	t.Cleanup(func() { availabilityProbeTimeout, availabilityFallbackTimeout = originalPrimary, originalFallback })
	availabilityProbeTimeout, availabilityFallbackTimeout = 100*time.Millisecond, 150*time.Millisecond
	runtime, _ := delayedAvailabilityRuntime(t, time.Millisecond, nil)
	started := make(chan struct{})
	// The mock API distinguishes the production selector from each probe lane.
	// Its proxy endpoint stays fixed, as it does in the actual rendered core.
	for _, lane := range runtime.backgrounds {
		var selected atomic.Value
		selected.Store("")
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if selected.Load().(string) == "a" {
				close(started)
				<-r.Context().Done()
				return
			}
			select {
			case <-started:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(proxy.Close)
		lane.opts.ProbeURL = proxy.URL
		lane.command = func(_ context.Context, _ time.Duration, _ string, args ...string) ([]byte, error) {
			if len(args) > 1 && args[1] == "bo" {
				selected.Store(args[len(args)-1])
				return nil, nil
			}
			if len(args) > 1 && args[1] == "bi" && args[len(args)-1] != lane.probeSelector {
				return selectorInfo("block"), nil
			}
			return selectorInfo(selected.Load().(string)), nil
		}
	}
	runtime.ctx = context.Background()
	controller := &healthController{runtime: runtime, state: healthState{"route": emergencyBestState()}}
	at := time.Now()
	measured, selected, err := controller.probeEmergencyCandidates(time.Unix(1000, 0), "route", []string{"b", "a"}, effectivePolicySettings{mode: "best", backup: 120, recoveryThreshold: 3})
	elapsed := time.Since(at)
	if err != nil || selected != "b" || measured["a"].OK || measured["a"].Failure != probeFailureTimeout || !measured["b"].OK || elapsed < availabilityProbeTimeout || elapsed > time.Second {
		t.Fatalf("preferred timeout did not release reserve within deadline: selected=%s elapsed=%s evidence=%v err=%v", selected, elapsed, measured, err)
	}
}

func emergencyDelay(value int) *int { return &value }
