package agentport

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRealSingleLaneQualityRecordsCompletionTime(t *testing.T) {
	original := healthTargets
	healthTargets = append(healthTargets[:0:0], original...)
	for i := range healthTargets {
		healthTargets[i].url = "http://probe.invalid/" + healthTargets[i].label
	}
	t.Cleanup(func() { healthTargets = original })
	for _, wrapped := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(25 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		}))
		primary := newXraySelectorRuntime(Options{ProbeURL: server.URL})
		primary.selectorMembers["outbound-health-probe"] = "direct-wan"
		started := time.Now()
		var evidence probeEvidence
		if wrapped {
			evidence = (&responsiveSelectorRuntime{selectorRuntime: primary}).Probe("direct-wan")
		} else {
			evidence = primary.Probe("direct-wan")
		}
		completed := time.Now()
		server.Close()
		if !evidence.OK || evidence.DelayMS == nil || evidence.ObservedAt.Before(started.Add(20*time.Millisecond)) || evidence.ObservedAt.After(completed) {
			t.Fatalf("wrapped=%t: real quality completion not recorded: at=%s start=%s end=%s evidence=%+v", wrapped, evidence.ObservedAt, started, completed, evidence)
		}
		if qualityTimestamp(started, evidence) != float64(evidence.ObservedAt.UnixNano())/1e9 {
			t.Fatal("tick start replaced the actual completion")
		}
	}
}
