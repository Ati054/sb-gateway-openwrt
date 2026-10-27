package agentport

import (
	"fmt"
	"testing"
	"time"
)

func recoveryEmergencyState(recovery int) *policyHealthState {
	item := emergencyBestState()
	item.Samples["a"] = []healthSample{
		{At: 980, OK: true, DelayMS: emergencyDelay(900)},
		{At: 985, OK: true, DelayMS: emergencyDelay(900)},
		{At: 990, OK: true, DelayMS: emergencyDelay(400)},
	}
	item.Samples["b"] = []healthSample{{At: 990, OK: true, DelayMS: emergencyDelay(640)}}
	item.MedianDelayMS["a"], item.MedianDelayMS["b"] = emergencyDelay(900), emergencyDelay(640)
	item.Recoveries["a"] = recovery
	item.LastProbeAt["a"], item.LastProbeAt["b"] = 990, 990
	return item
}

func TestEmergencyBestDoesNotRequireRecoveryConfirmations(t *testing.T) {
	for recovery := 0; recovery <= 3; recovery++ {
		for _, reachable := range []bool{false, true} {
			t.Run(fmt.Sprintf("recovery=%d/reachable=%t", recovery, reachable), func(t *testing.T) {
				item := recoveryEmergencyState(recovery)
				fast, want := failedEvidence(), "b"
				if reachable {
					fast, want = successfulEvidence(400), "a"
				}
				base := &fakeSelectorRuntime{probes: map[string]probeEvidence{"a": fast, "b": successfulEvidence(640)}}
				controller := &healthController{runtime: &reverseParallelRuntime{base}, state: healthState{"route": item}}
				measured, selected, err := controller.probeEmergencyCandidates(time.Unix(1000, 0), "route", []string{"a", "b"},
					effectivePolicySettings{mode: "best", backup: 120, recoveryThreshold: 3})
				if err != nil || selected != want || len(measured) != 2 || len(base.selections) != 1 {
					t.Fatalf("recovery changed emergency preference or bypassed a current check: selected=%s want=%s measured=%v err=%v", selected, want, measured, err)
				}
				if got := knownFreshReserve(time.Unix(1000, 0), "active", []string{"b", "a"}, "best", nil, item,
					effectivePolicySettings{backup: 120, recoveryThreshold: 3}); got != "a" {
					t.Fatalf("single emergency slot would skip the fastest fresh reserve: %s", got)
				}
			})
		}
	}
}

func TestEmergencyFallbackUsesLatestQualityWithoutRecoveryTier(t *testing.T) {
	for _, selected := range []string{"active", "block"} {
		for _, mode := range []string{"best", "priority"} {
			t.Run(selected+"/"+mode, func(t *testing.T) {
				item := recoveryEmergencyState(1)
				item.AvailabilityFailures["active"] = 3
				settings := effectivePolicySettings{mode: mode, active: 60, backup: 120, failureThreshold: 3, recoveryThreshold: 3}
				desired, _ := selectDesired(time.Unix(1000, 0), mode, selected, []string{"active", "a", "b"},
					map[string]int{"a": 0, "b": 1}, nil, item.MedianDelayMS,
					map[string]probeEvidence{"a": {OK: true}, "b": {OK: true}}, item.QualityOK, item.AvailabilityOK, item, settings)
				if desired != "a" {
					t.Fatalf("old median/recovery tier overrode current successful reserve: selected=%s", desired)
				}
			})
		}
	}
}

func TestEmergencyFallbackIgnoresFailedAndExpiredQuality(t *testing.T) {
	for _, expired := range []bool{false, true} {
		item := recoveryEmergencyState(0)
		item.AvailabilityFailures["active"] = 3
		measured := map[string]probeEvidence{"a": failedEvidence(), "b": {OK: true}}
		if expired {
			item.Samples["a"][2].At = 800
			measured["a"] = probeEvidence{OK: true}
		}
		desired, _ := selectDesired(time.Unix(1000, 0), "best", "active", []string{"active", "a", "b"}, nil, nil,
			item.MedianDelayMS, measured, item.QualityOK, item.AvailabilityOK, item,
			effectivePolicySettings{backup: 120, failureThreshold: 3, recoveryThreshold: 3})
		if desired != "b" {
			t.Fatalf("expired=%t: unusable quality preferred over a current successful reserve: %s", expired, desired)
		}
	}
}

func TestEmergencyPreferenceRequiresCurrentAvailabilityNotPastRecovery(t *testing.T) {
	item := recoveryEmergencyState(0)
	item.AvailabilityOK["a"], item.QualityOK["a"] = false, false
	item.AvailabilityFailures["a"] = 3
	base := &fakeSelectorRuntime{probes: map[string]probeEvidence{
		"a": {OK: true, QualityUnmeasured: true}, "b": {OK: true, QualityUnmeasured: true},
	}}
	controller := &healthController{runtime: &reverseParallelRuntime{base}, state: healthState{"route": item}}
	measured, selected, err := controller.probeEmergencyCandidates(time.Unix(1000, 0), "route", []string{"a", "b"},
		effectivePolicySettings{mode: "best", backup: 120, recoveryThreshold: 3})
	if err != nil || selected != "a" || !measured["a"].OK || len(base.selections) != 1 {
		t.Fatalf("past availability/recovery overrode a current success: selected=%s err=%v", selected, err)
	}
	item.AvailabilityFailures["active"] = 3
	desired, _ := selectDesired(time.Unix(1000, 0), "best", "active", []string{"active", "a", "b"}, nil, nil,
		item.MedianDelayMS, measured, item.QualityOK, item.AvailabilityOK, item,
		effectivePolicySettings{backup: 120, failureThreshold: 3, recoveryThreshold: 3})
	if desired != "a" {
		t.Fatal("availability-only evidence discarded a fresh HTTPS preference")
	}
}

func TestHealthyActiveStillRequiresReserveRecovery(t *testing.T) {
	item := recoveryEmergencyState(1)
	item.Samples["active"] = []healthSample{{At: 990, OK: true, DelayMS: emergencyDelay(800)}}
	item.MedianDelayMS["active"] = emergencyDelay(800)
	item.AvailabilityOK["active"], item.QualityOK["active"] = true, true
	desired, _ := selectDesired(time.Unix(1000, 0), "best", "active", []string{"active", "a"}, nil, nil,
		item.MedianDelayMS, map[string]probeEvidence{"active": successfulEvidence(800), "a": successfulEvidence(400)},
		item.QualityOK, item.AvailabilityOK, item,
		effectivePolicySettings{active: 60, backup: 120, failureThreshold: 3, recoveryThreshold: 3, improvement: 200})
	if desired != "active" {
		t.Fatal("emergency recovery relaxation leaked into planned optimization")
	}
}

func TestEmergencyControllerKeepsFastRecoveringReserveFirst(t *testing.T) {
	for _, batch := range []int{1, 3} {
		t.Run(fmt.Sprintf("batch=%d", batch), func(t *testing.T) {
			controller, runtime, item := livenessFixture(t, "best")
			contract := runtime.pool.HealthPolicies["europe"]
			contract.Candidates = []string{"de", "fr", "nl"}
			contract.Nodes["fr"] = healthNode{Label: "Slow reserve"}
			contract.Policy.ProbeBatchSize = batch
			runtime.pool.HealthPolicies["europe"] = contract
			item.CandidateSignature = "de\nfr\nnl"
			item.Shortlist = []string{"de", "fr"}
			for node, delay := range map[string]int{"nl": 400, "fr": 640} {
				value := delay
				item.Samples[node] = []healthSample{{At: 1000, OK: true, DelayMS: &value}}
				item.LastProbeAt[node] = 1000
				item.AvailabilityOK[node], item.QualityOK[node] = true, true
				item.Recoveries[node] = 3
				runtime.probes[node] = successfulEvidence(delay)
			}
			item.Recoveries["nl"] = 1
			item.MedianDelayMS["nl"], item.MedianDelayMS["fr"] = emergencyDelay(900), emergencyDelay(640)
			runtime.probes["de"] = probeEvidence{Failure: probeFailureTimeout}
			before := len(runtime.availabilityCalls)
			for _, second := range []int64{1010, 1012} {
				if err := controller.Tick(time.Unix(second, 0)); err != nil {
					t.Fatal(err)
				}
			}
			if item.Selected != "nl" || !item.RuntimeConfirmed || hasSelection(runtime.selections, "europe", "fr") {
				t.Fatalf("controller skipped fast recovering reserve: selected=%s switches=%v", item.Selected, runtime.selections)
			}
			if len(runtime.availabilityCalls)-before > 2+batch {
				t.Fatal("recovery fix exceeded the existing emergency probe budget")
			}
		})
	}
}
