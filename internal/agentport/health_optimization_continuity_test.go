package agentport

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func alternatingReserveController(t *testing.T) (*healthController, *policyHealthState, *fakeSelectorRuntime) {
	t.Helper()
	controller, item, runtime := stagedOptimizationController(t)
	clearOptimizationCandidate(item)
	contract := runtime.pool.HealthPolicies["europe"]
	contract.Policy.ProbeBatchSize = 3
	contract.Candidates = append(contract.Candidates, "other")
	contract.Nodes["other"] = healthNode{Label: "Other"}
	runtime.pool.HealthPolicies["europe"] = contract
	item.CandidateSignature = strings.Join(contract.Candidates, "\n")
	item.AvailabilityOK["other"], item.QualityOK["other"] = true, true
	item.Recoveries["other"] = 3
	item.LastProbeAt["other"] = 1000
	delay := 401
	item.Samples["other"] = []healthSample{{At: 1000, OK: true, DelayMS: &delay}}
	runtime.probes["other"] = successfulEvidence(delay)
	return controller, item, runtime
}

func TestURLTestAlternatingReservesCompletesConfirmation(t *testing.T) {
	for _, difference := range []int{1, 20, 100} {
		t.Run(strconv.Itoa(difference), func(t *testing.T) {
			controller, item, runtime := alternatingReserveController(t)
			for round := 0; round < 8; round++ {
				first, second := 400, 400+difference
				if round%2 == 1 {
					first, second = second, first
				}
				runtime.probes["active"] = successfulEvidence(600)
				runtime.probes["reserve"] = successfulEvidence(first)
				runtime.probes["other"] = successfulEvidence(second)
				if err := controller.Tick(time.Unix(int64(1060+round*60), 0)); err != nil {
					t.Fatal(err)
				}
				if round == 0 {
					if item.Selected != "active" || item.OptimizationCandidate != "reserve" || item.OptimizationChecks != 1 {
						t.Fatalf("first win must await confirmation: selected=%s candidate=%s checks=%d", item.Selected, item.OptimizationCandidate, item.OptimizationChecks)
					}
				} else if item.Selected != "reserve" || round == 1 && item.OptimizationChecks != 0 {
					t.Fatalf("round %d: alternating reserves prevented confirmation or caused another switch: selected=%s candidate=%s checks=%d", round+1, item.Selected, item.OptimizationCandidate, item.OptimizationChecks)
				}
			}
			if hasSelection(runtime.selections, "europe", "other") || item.LastSwitchAt != time.Unix(1120, 0).UTC().Format(time.RFC3339) {
				t.Fatal("alternating one-round improvements must not cause further switches")
			}
		})
	}
}

func TestURLTestConfirmedCandidateDoesNotPreventLaterImprovement(t *testing.T) {
	controller, item, runtime := alternatingReserveController(t)
	runtime.probes["active"] = successfulEvidence(600)
	runtime.probes["reserve"] = successfulEvidence(400)
	if err := controller.Tick(time.Unix(1060, 0)); err != nil {
		t.Fatal(err)
	}
	runtime.probes["reserve"] = successfulEvidence(500)
	runtime.probes["other"] = successfulEvidence(400)
	for round, at := range []int64{1120, 1180, 1240} {
		if err := controller.Tick(time.Unix(at, 0)); err != nil {
			t.Fatal(err)
		}
		want := "reserve"
		if round == 2 {
			want = "other"
		}
		if item.Selected != want {
			t.Fatalf("at %d: selected=%s, want=%s", at, item.Selected, want)
		}
		if round == 1 && (item.OptimizationBaseline != "reserve" || item.OptimizationCandidate != "other" || item.OptimizationChecks != 1) {
			t.Fatal("later improvement must obtain fresh confirmations against the new active")
		}
	}
}

func TestURLTestPendingCandidateMustRemainQualified(t *testing.T) {
	for _, scenario := range []string{"lost-improvement", "unavailable", "removed", "expired-confirmation"} {
		t.Run(scenario, func(t *testing.T) {
			controller, item, runtime := alternatingReserveController(t)
			runtime.probes["active"] = successfulEvidence(600)
			runtime.probes["reserve"] = successfulEvidence(400)
			if err := controller.Tick(time.Unix(1060, 0)); err != nil {
				t.Fatal(err)
			}
			if item.OptimizationCandidate != "reserve" || item.OptimizationChecks != 1 {
				t.Fatal("first candidate was not staged")
			}
			next := int64(1120)
			runtime.probes["reserve"] = successfulEvidence(401)
			runtime.probes["other"] = successfulEvidence(400)
			switch scenario {
			case "lost-improvement":
				// Exactly the configured 50 ms is insufficient.
				runtime.probes["reserve"] = successfulEvidence(550)
			case "unavailable":
				runtime.probes["reserve"] = probeEvidence{}
			case "removed":
				contract := runtime.pool.HealthPolicies["europe"]
				contract.Candidates = []string{"active", "other"}
				runtime.pool.HealthPolicies["europe"] = contract
			case "expired-confirmation":
				next = 1240
			}
			if err := controller.Tick(time.Unix(next, 0)); err != nil {
				t.Fatal(err)
			}
			if item.Selected != "active" || item.OptimizationCandidate != "other" || item.OptimizationChecks != 1 {
				t.Fatalf("invalid pending candidate retained or transferred its win: selected=%s candidate=%s checks=%d", item.Selected, item.OptimizationCandidate, item.OptimizationChecks)
			}
			// The new candidate keeps its own win even if the old one recovers.
			runtime.probes["reserve"] = successfulEvidence(400)
			runtime.probes["other"] = successfulEvidence(401)
			if err := controller.Tick(time.Unix(next+60, 0)); err != nil {
				t.Fatal(err)
			}
			if item.Selected != "other" || item.LastSwitchReason != "meaningfully-faster" {
				t.Fatalf("new candidate did not complete its own confirmation: selected=%s reason=%s", item.Selected, item.LastSwitchReason)
			}
		})
	}
}
