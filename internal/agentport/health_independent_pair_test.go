package agentport

import (
	"testing"
	"time"
)

func TestURLTestSecondConfirmationRequiresNewActiveMeasurement(t *testing.T) {
	for _, scenario := range []string{"cached-active", "same-second", "new-cached-active", "new-paired-active"} {
		t.Run(scenario, func(t *testing.T) {
			item := newPolicyHealthState()
			p := effectivePolicySettings{active: 60, improvement: 50}
			delay := 600
			item.Samples["active"] = []healthSample{{At: 999, OK: true, DelayMS: &delay}}
			check := func(at int64, measured map[string]probeEvidence) string {
				now := time.Unix(at, 0)
				comparison := regularOptimizationComparison(now, "active", "reserve", measured, item, true, true, p)
				desired, _ := gatePlannedOptimization(now, "active", "reserve", "meaningfully-faster", comparison, item, p)
				return desired
			}
			if got := check(1000, map[string]probeEvidence{"reserve": successfulEvidence(400)}); got != "active" || item.OptimizationChecks != 1 {
				t.Fatal("first split pair must await a second independent confirmation")
			}
			measured := map[string]probeEvidence{"reserve": successfulEvidence(400)}
			want := "reserve"
			switch scenario {
			case "cached-active":
				want = "active"
			case "same-second":
				item.Samples["active"][0].At = 1000
				want = "active"
			case "new-cached-active":
				item.Samples["active"][0].At = 1030
			case "new-paired-active":
				measured["active"] = successfulEvidence(600)
			}
			if got := check(1060, measured); got != want {
				t.Fatalf("second confirmation selected %q, want %q: checks=%d", got, want, item.OptimizationChecks)
			}
			if want == "active" {
				if item.OptimizationChecks != 1 {
					t.Fatal("waiting for new active evidence must retain the first confirmation")
				}
				item.Samples["active"][0].At = 1090
				if got := check(1120, map[string]probeEvidence{"reserve": successfulEvidence(400)}); got != "reserve" {
					t.Fatal("a new split pair did not complete confirmation")
				}
			}
		})
	}
}

func TestURLTestPendingPairDoesNotHideFreshFailure(t *testing.T) {
	for _, failed := range []string{"active", "reserve", "improvement"} {
		t.Run(failed, func(t *testing.T) {
			item := newPolicyHealthState()
			p := effectivePolicySettings{active: 60, improvement: 50}
			delay := 600
			item.Samples["active"] = []healthSample{{At: 999, OK: true, DelayMS: &delay}}
			now := time.Unix(1000, 0)
			first := regularOptimizationComparison(now, "active", "reserve", map[string]probeEvidence{"reserve": successfulEvidence(400)}, item, true, true, p)
			gatePlannedOptimization(now, "active", "reserve", "meaningfully-faster", first, item, p)
			measured := map[string]probeEvidence{"reserve": successfulEvidence(400)}
			switch failed {
			case "active":
				measured["active"] = probeEvidence{}
			case "reserve":
				measured["reserve"] = probeEvidence{}
			case "improvement":
				measured["reserve"] = successfulEvidence(550)
			}
			now = time.Unix(1060, 0)
			second := regularOptimizationComparison(now, "active", "reserve", measured, item, true, true, p)
			if second == nil || second.Result != optimizationLoss {
				t.Fatal("waiting for independent evidence hid a failed comparison")
			}
			desired, _ := gatePlannedOptimization(now, "active", "reserve", "meaningfully-faster", second, item, p)
			if desired != "active" || item.OptimizationChecks != 0 {
				t.Fatal("failed comparison retained a pending win")
			}
		})
	}
}
