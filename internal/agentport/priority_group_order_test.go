package agentport

import (
	"testing"
	"time"
)

func TestPriorityGroupOrderIgnoresSubscriptionMemberReorder(t *testing.T) {
	if !priorityGroupOrderChanged("", []string{"fr1"}, map[string]int{"fr1": 0}) {
		t.Fatal("missing saved order was trusted")
	}
	groups := map[string]int{"fr1": 0, "fr2": 0, "pl": 1, "gb": 2}
	if priorityGroupOrderChanged("fr1\nfr2\npl\ngb", []string{"fr2", "fr1", "pl", "gb"}, groups) {
		t.Fatal("member reordering changed location priority")
	}
	if !priorityGroupOrderChanged("pl\nfr1\nfr2\ngb", []string{"fr2", "fr1", "pl", "gb"}, groups) {
		t.Fatal("location reordering was ignored")
	}
	contract := healthPolicyContract{Mode: "priority", Candidates: []string{"fr2", "fr1", "pl", "gb"}, Groups: []healthGroup{
		{Selector: "location:FR", Members: []string{"fr2", "fr1"}},
		{Selector: "country:PL", Members: []string{"pl"}},
		{Selector: "country:GB", Members: []string{"gb"}},
	}}
	item := newPolicyHealthState()
	ensureHealthMaps(item)
	item.Mode, item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "priority", "fr1", "fr1", true
	item.CandidateSignature = "fr1\nfr2\npl\ngb"
	item.AvailabilityOK = map[string]bool{"fr1": true, "fr2": true}
	item.LastSwitchReason = "fresh-path-available"
	if !restorableWorkingSelection(contract, item) {
		t.Fatal("same-location reordering invalidated remembered working selection")
	}
	item.CandidateSignature = "pl\nfr1\nfr2\ngb"
	if restorableWorkingSelection(contract, item) {
		t.Fatal("changed location priority kept an obsolete ordering")
	}
}

func TestPriorityMemberReorderKeepsActiveHandlerOnReload(t *testing.T) {
	controller, runtime, item := livenessFixture(t, "priority")
	contract := runtime.pool.HealthPolicies["europe"]
	contract.Candidates = []string{"nl", "de"}
	contract.Groups = []healthGroup{{Selector: "location:shared", Members: []string{"nl", "de"}}}
	runtime.pool.HealthPolicies["europe"] = contract
	item.AvailabilityOK = map[string]bool{"de": true, "nl": true}
	item.CandidateSignature = "de\nnl"
	before := len(runtime.selections)
	if err := controller.Tick(time.Unix(1001, 0)); err != nil {
		t.Fatal(err)
	}
	if item.Selected != "de" || !item.RuntimeConfirmed || len(runtime.selections) != before {
		t.Fatalf("member reorder withdrew active handler: selected=%s selections=%v", item.Selected, runtime.selections)
	}
}
