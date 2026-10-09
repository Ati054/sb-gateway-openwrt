// Adapted from SB Gateway 1.6.45 (AGPL-3.0).
package agentport

import (
	"errors"
	"testing"
)

type unavailableStartupObservation struct{ *fakeSelectorRuntime }

func (*unavailableStartupObservation) Current(string) (string, error) {
	return "", errors.New("Xray selector state is unavailable")
}

func TestWorkingSelectionRequiresBothReadbackAndAvailability(t *testing.T) {
	contract := healthFixture(false).HealthPolicies["europe"]
	item := newPolicyHealthState()
	item.Mode, item.CandidateSignature = contract.Mode, "de\nnl"
	item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "nl", "nl", true
	if rememberWorkingSelection(contract, item) {
		t.Fatal("unprobed leaf remembered")
	}
	item.AvailabilityOK = map[string]bool{"nl": true}
	if !rememberWorkingSelection(contract, item) {
		t.Fatal("working leaf not remembered")
	}
	if rememberWorkingSelection(contract, item) {
		t.Fatal("unchanged evidence must not trigger writes")
	}
	item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "de", "", false
	item.AvailabilityOK["de"] = true
	if rememberWorkingSelection(contract, item) || item.LastWorkingSelection.Selected != "nl" {
		t.Fatal("unconfirmed selection replaced working evidence")
	}
}

// A control-API error must not erase durable evidence of a working leaf.
