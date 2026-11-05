package agentport

import (
	"fmt"
	"testing"
	"time"
)

func completedQualityFixture(t *testing.T, mode string, seconds int) (*healthController, *fakeSelectorRuntime, *policyHealthState, time.Time) {
	t.Helper()
	controller, runtime, item := livenessFixture(t, mode)
	contract := runtime.pool.HealthPolicies["europe"]
	contract.Policy.ActiveLivenessSeconds = seconds
	runtime.pool.HealthPolicies["europe"] = contract
	observed := time.Unix(1060, 900_000_000)
	evidence := successfulEvidence(100)
	evidence.ObservedAt = observed
	runtime.probes["de"] = evidence
	if err := controller.Tick(observed); err != nil {
		t.Fatal(err)
	}
	if item.Selected != "de" {
		t.Fatal("fixture changed active node")
	}
	runtime.availabilityCalls = nil
	return controller, runtime, item, observed
}

func TestCompletedQualityProofKeepsFullLivenessInterval(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		for _, seconds := range []int{3, 10, 30} {
			t.Run(fmt.Sprintf("%s/%ds", mode, seconds), func(t *testing.T) {
				controller, runtime, item, observed := completedQualityFixture(t, mode, seconds)
				contract := runtime.pool.HealthPolicies["europe"]
				interval := time.Duration(seconds) * time.Second
				for _, elapsed := range []time.Duration{interval - 250*time.Millisecond, interval - time.Millisecond} {
					if _, err := controller.tickActiveAvailability(observed.Add(elapsed), "europe", contract, item); err != nil {
						t.Fatal(err)
					}
					if len(runtime.availabilityCalls) != 0 {
						t.Fatalf("rounded quality proof caused an early probe at %s", elapsed)
					}
				}
				if _, err := controller.tickActiveAvailability(observed.Add(interval), "europe", contract, item); err != nil {
					t.Fatal(err)
				}
				if len(runtime.availabilityCalls) != 1 {
					t.Fatal("liveness did not resume at the complete interval")
				}
			})
		}
	}
}

func TestFreshQualityProofPreservesForcedAndFailureChecks(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		t.Run(mode+"/forced", func(t *testing.T) {
			controller, runtime, item, observed := completedQualityFixture(t, mode, 30)
			controller.forceLiveness = map[string]bool{"europe": true}
			if _, err := controller.tickActiveAvailability(observed.Add(time.Millisecond), "europe", runtime.pool.HealthPolicies["europe"], item); err != nil {
				t.Fatal(err)
			}
			if len(runtime.availabilityCalls) != 1 || controller.forceLiveness["europe"] {
				t.Fatal("fresh quality proof delayed or retained the forced check")
			}
		})
		t.Run(mode+"/failure-retry", func(t *testing.T) {
			controller, runtime, item, observed := completedQualityFixture(t, mode, 30)
			contract := runtime.pool.HealthPolicies["europe"]
			item.AvailabilityFailures["de"] = 1
			item.FailureClass["de"] = string(probeFailureTimeout)
			runtime.probes["de"] = failedEvidence()
			lastAvailability := observed.Add(time.Second)
			controller.livenessAt["europe"] = lastAvailability
			retry := time.Duration(policySettings(contract.Policy, mode).failureRetry) * time.Second
			if _, err := controller.tickActiveAvailability(lastAvailability.Add(retry-time.Millisecond), "europe", contract, item); err != nil {
				t.Fatal(err)
			}
			if len(runtime.availabilityCalls) != 0 {
				t.Fatal("failure confirmation ran before the retry interval")
			}
			if _, err := controller.tickActiveAvailability(lastAvailability.Add(retry), "europe", contract, item); err != nil {
				t.Fatal(err)
			}
			if len(runtime.availabilityCalls) != 1 || item.AvailabilityFailures["de"] != 2 || item.Selected != "de" {
				t.Fatal("fresh quality proof changed failure confirmation or retry cadence")
			}
		})
	}
}
