package agentport

import "testing"

func TestSavedWorkingSelectionAllowsPendingButRejectsConfirmedOrEdited(t *testing.T) {
	for _, mode := range []string{"best", "priority"} {
		t.Run(mode, func(t *testing.T) {
			contract := healthFixture(false).HealthPolicies["europe"]
			contract.Mode = mode
			item := newPolicyHealthState()
			item.Mode = mode
			item.Selected = "nl"
			item.CandidateSignature = "de\nnl"
			item.LastWorkingSelection = &workingSelection{"nl", mode, "de\nnl"}
			item.AvailabilityFailures["nl"] = 1
			if !restorableWorkingSelection(contract, item) {
				t.Fatal("pending failure erased saved working selection")
			}
			item.AvailabilityFailures["nl"] = policySettings(contract.Policy, mode).failureThreshold
			if restorableWorkingSelection(contract, item) {
				t.Fatal("confirmed outage restored")
			}
			item.AvailabilityFailures["nl"] = 0
			if mode == "priority" {
				contract.Candidates = []string{"nl", "de"}
				if restorableWorkingSelection(contract, item) {
					t.Fatal("explicit reorder ignored")
				}
			}
			contract.Candidates = []string{"de"}
			if restorableWorkingSelection(contract, item) {
				t.Fatal("removed candidate restored")
			}
		})
	}
}
