package agentport

import (
	"testing"
	"time"
)

func TestMaintenanceDefersQualityButKeepsActiveLiveness(t *testing.T) {
	c, runtime, item := livenessFixture(t, "best")
	c.opts.BackgroundBusy = func() bool { return true }
	before := len(runtime.probeCalls)
	if err := c.Tick(time.Unix(1065, 0)); err != nil {
		t.Fatal(err)
	}
	if len(runtime.probeCalls) != before {
		t.Fatal("routine quality overlapped heavy update", runtime.probeCalls)
	}
	if len(runtime.availabilityCalls) == 0 {
		t.Fatal("active liveness was paused")
	}
	c.opts.BackgroundBusy = func() bool { return false }
	if err := c.Tick(time.Unix(1070, 0)); err != nil {
		t.Fatal(err)
	}
	if len(runtime.probeCalls) == before {
		t.Fatal("quality did not resume")
	}
	_ = item
}

func TestMaintenanceDoesNotBlockEmergencyReserve(t *testing.T) {
	c, runtime, item := livenessFixture(t, "best")
	c.opts.BackgroundBusy = func() bool { return true }
	runtime.probes["de"] = probeEvidence{}
	for _, second := range []int64{1065, 1068, 1070, 1072} {
		if err := c.Tick(time.Unix(second, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if item.Selected != "nl" {
		t.Fatal("maintenance blocked emergency recovery", item.Selected, runtime.probeCalls, runtime.availabilityCalls)
	}
}

func TestBusyCPUDoesNotTurnUncertainAPIIntoFullQualityScan(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		t.Run(mode, func(t *testing.T) {
			c, runtime, item := livenessFixture(t, mode)
			c.opts.BackgroundBusy = func() bool { return true }
			item.LastWorkingSelection = &workingSelection{Selected: item.Selected, Mode: item.Mode, CandidateSignature: item.CandidateSignature}
			item.RuntimeConfirmed = false
			item.RuntimeSelected = ""
			before := len(runtime.probeCalls)
			if err := c.Tick(time.Unix(1065, 0)); err != nil {
				t.Fatal(err)
			}
			if len(runtime.probeCalls) != before {
				t.Fatal("uncertain control reply amplified background work", runtime.probeCalls)
			}
			if len(runtime.availabilityCalls) == 0 {
				t.Fatal("active liveness was deferred")
			}
		})
	}
}

func TestBusyCPUDoesNotDeferUnreconciledConfiguration(t *testing.T) {
	c, runtime, _ := livenessFixture(t, "best")
	c.opts.BackgroundBusy = func() bool { return true }
	delete(c.reconciled, "europe")
	before := len(runtime.probeCalls)
	if err := c.Tick(time.Unix(1065, 0)); err != nil {
		t.Fatal(err)
	}
	if len(runtime.probeCalls) == before {
		t.Fatal("busy CPU postponed reconciliation of a changed contract")
	}
}
