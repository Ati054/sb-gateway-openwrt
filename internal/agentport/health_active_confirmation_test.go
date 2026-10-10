package agentport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestActiveTimeoutConfirmationAcceptsSlowPathAndJoinsOrigins(t *testing.T) {
	original, primary, fallback, confirm := healthTargets, availabilityProbeTimeout, availabilityFallbackTimeout, availabilityConfirmationTimeout
	t.Cleanup(func() {
		healthTargets, availabilityProbeTimeout, availabilityFallbackTimeout, availabilityConfirmationTimeout = original, primary, fallback, confirm
	})
	healthTargets = append(healthTargets[:0:0], original...)
	for i := range healthTargets {
		healthTargets[i].url = "http://probe.invalid/" + healthTargets[i].label
	}
	availabilityProbeTimeout, availabilityFallbackTimeout, availabilityConfirmationTimeout = 200*time.Millisecond, 300*time.Millisecond, 3*time.Second
	var started, finished atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		defer finished.Add(1)
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			if r.URL.Path == "/gstatic-204" {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	runtime := newXraySelectorRuntime(Options{ProbeURL: server.URL})
	runtime.selectorMembers["outbound-health-probe"] = "direct-wan"
	if e := runtime.ProbeAvailability("direct-wan"); e.OK || e.Failure != probeFailureTimeout {
		t.Fatalf("short proof unexpectedly succeeded: %+v", e)
	}
	prior := started.Load()
	e := runtime.ProbeAvailabilityConfirmation("direct-wan")
	if !e.OK || !e.QualityUnmeasured || e.PrimaryQualityFailed || started.Load()-prior != 3 {
		t.Fatalf("confirmation lost slow reachable path or treated it as quality: %+v", e)
	}
	deadline := time.Now().Add(time.Second)
	for finished.Load() != started.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if finished.Load() != started.Load() {
		t.Fatal("confirmation left canceled origins running")
	}
}

func TestActiveTimeoutConfirmationStillRejectsDeadPath(t *testing.T) {
	original, confirm := healthTargets, availabilityConfirmationTimeout
	t.Cleanup(func() { healthTargets, availabilityConfirmationTimeout = original, confirm })
	healthTargets = append(healthTargets[:0:0], original...)
	for i := range healthTargets {
		healthTargets[i].url = "http://probe.invalid/" + healthTargets[i].label
	}
	availabilityConfirmationTimeout = 500 * time.Millisecond
	var started atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started.Add(1); <-r.Context().Done() }))
	defer server.Close()
	runtime := newXraySelectorRuntime(Options{ProbeURL: server.URL})
	runtime.selectorMembers["outbound-health-probe"] = "direct-wan"
	at := time.Now()
	e := runtime.ProbeAvailabilityConfirmation("direct-wan")
	if e.OK || e.LocalFailure || e.Failure != probeFailureTimeout || len(e.TargetFailures) != 3 || started.Load() != 3 || time.Since(at) < availabilityConfirmationTimeout {
		t.Fatalf("incomplete negative confirmation: %+v", e)
	}
}

type activeConfirmationFixture struct {
	*fakeSelectorRuntime
	confirmation probeEvidence
	calls        int
}

func (r *activeConfirmationFixture) ProbeAvailabilityConfirmation(candidate string) probeEvidence {
	r.calls++
	return r.confirmation
}

func TestActiveConfirmationOnlyRunsAfterPendingTimeout(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		for _, healthy := range []bool{true, false} {
			t.Run(mode+map[bool]string{true: "/reachable", false: "/dead"}[healthy], func(t *testing.T) {
				controller, base, item := pendingFailureFixture(t, mode)
				confirmation := probeEvidence{Failure: probeFailureTimeout}
				if healthy {
					confirmation = successfulEvidence(3000)
					confirmation.QualityUnmeasured = true
				}
				runtime := &activeConfirmationFixture{fakeSelectorRuntime: base, confirmation: confirmation}
				controller.runtime = &responsiveSelectorRuntime{selectorRuntime: runtime, ctx: context.Background()}
				if err := controller.Tick(time.Unix(1058, 0)); err != nil {
					t.Fatal(err)
				}
				if runtime.calls != 0 || item.AvailabilityFailures["de"] != 1 || item.Selected != "de" {
					t.Fatal("ordinary fast lane was expanded")
				}
				if err := controller.Tick(time.Unix(1060, 0)); err != nil {
					t.Fatal(err)
				}
				want := "nl"
				if healthy {
					want = "de"
				}
				if runtime.calls != 1 || item.Selected != want || len(base.probeCalls) != 0 {
					t.Fatalf("wrong timeout confirmation outcome: calls=%d selected=%s quality=%v", runtime.calls, item.Selected, base.probeCalls)
				}
				if healthy && item.AvailabilityFailures["de"] != 0 {
					t.Fatal("reachable active retained failures")
				}
			})
		}
	}
}

// The quality path remains healthy, but its first cold availability deadline
// expires. A restart must prove the remembered route with the full deadline.
type coldAvailabilityFixture struct{ *activeConfirmationFixture }

func (r *coldAvailabilityFixture) ProbeAvailability(candidate string) probeEvidence {
	r.availabilityCalls = append(r.availabilityCalls, candidate)
	return probeEvidence{Failure: probeFailureTimeout}
}
func TestWarmBlockedCoreConfirmsRememberedPathAfterTimeout(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		t.Run(mode, func(t *testing.T) {
			controller, base, item := livenessFixture(t, mode)
			item.LastWorkingSelection = &workingSelection{"de", mode, "de\nnl"}
			controller.warmStarted = map[string]bool{}
			base.current["europe"] = "block"
			inner := &activeConfirmationFixture{fakeSelectorRuntime: base, confirmation: successfulEvidence(3000)}
			inner.confirmation.QualityUnmeasured = true
			runtime := &coldAvailabilityFixture{inner}
			controller.runtime = runtime
			if err := controller.Tick(time.Unix(1010, 0)); err != nil {
				t.Fatal(err)
			}
			if inner.calls != 1 || !hasSelection(base.selections, "europe", "de") || item.Selected != "de" || !item.RuntimeConfirmed {
				t.Fatalf("cold timeout discarded remembered reachable path: calls=%d selected=%s", inner.calls, item.Selected)
			}
		})
	}
}
