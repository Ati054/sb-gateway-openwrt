package agentport

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAvailabilityHedgePreservesLatePrimary(t *testing.T) {
	original, primary, fallback := healthTargets, availabilityProbeTimeout, availabilityFallbackTimeout
	t.Cleanup(func() {
		healthTargets, availabilityProbeTimeout, availabilityFallbackTimeout = original, primary, fallback
	})
	healthTargets = append(original[:0:0], original...)
	for i := range healthTargets {
		healthTargets[i].url = "http://probe.invalid/" + healthTargets[i].label
	}
	availabilityProbeTimeout, availabilityFallbackTimeout = 2*time.Second, 3*time.Second
	var started, canceled atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		if r.URL.Path != "/gstatic-204" {
			<-r.Context().Done()
			canceled.Add(1)
			return
		}
		timer := time.NewTimer(3400 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			w.WriteHeader(204)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	runtime := newXraySelectorRuntime(Options{ProbeURL: server.URL})
	runtime.selectorMembers["outbound-health-probe"] = "direct-wan"
	evidence := runtime.ProbeAvailability("direct-wan")
	if !evidence.OK || evidence.Targets["gstatic-204"] == nil || !evidence.QualityUnmeasured || started.Load() != 3 || len(evidence.TargetFailures) != 0 {
		t.Fatalf("late primary lost or siblings recorded as failures: %+v calls=%d", evidence, started.Load())
	}
	until := time.Now().Add(time.Second)
	for canceled.Load() != 2 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if canceled.Load() != 2 {
		t.Fatal("hedged siblings did not stop")
	}
}

func TestAvailabilityHedgeKeepsSharedDeadlineAndFullConfirmation(t *testing.T) {
	original, primary, fallback, confirmation := healthTargets, availabilityProbeTimeout, availabilityFallbackTimeout, availabilityConfirmationTimeout
	t.Cleanup(func() {
		healthTargets, availabilityProbeTimeout, availabilityFallbackTimeout, availabilityConfirmationTimeout = original, primary, fallback, confirmation
	})
	healthTargets = append(original[:0:0], original...)
	for i := range healthTargets {
		healthTargets[i].url = "http://probe.invalid/" + healthTargets[i].label
	}
	availabilityProbeTimeout, availabilityFallbackTimeout, availabilityConfirmationTimeout = 2*time.Second, 3*time.Second, 5*time.Second
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gstatic-204" {
			<-r.Context().Done()
			return
		}
		timer := time.NewTimer(3400 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			w.WriteHeader(200)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	runtime := newXraySelectorRuntime(Options{ProbeURL: server.URL})
	runtime.selectorMembers["outbound-health-probe"] = "direct-wan"
	start := time.Now()
	first := runtime.ProbeAvailability("direct-wan")
	if first.OK || first.Failure != probeFailureTimeout || len(first.TargetFailures) != 3 || time.Since(start) > 6500*time.Millisecond {
		t.Fatalf("round deadline/evidence wrong: %+v elapsed=%v", first, time.Since(start))
	}
	if second := runtime.ProbeAvailabilityConfirmation("direct-wan"); !second.OK || !second.QualityUnmeasured {
		t.Fatalf("hedging shortened confirmation: %+v", second)
	}
}
