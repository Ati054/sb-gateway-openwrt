package settings

import "testing"

func TestCustomRuleHostIPsAreNormalizedBeforeRoutingValidation(t *testing.T) {
	values := map[string]string{"resproxy.@rule[0].id": "host-rule", "resproxy.@rule[0].action": "proxy", "resproxy.@rule[0].cidrs": "195.82.146.214 2001:db8::1", "resproxy.@policy[0].id": "main", "resproxy.@policy[0].rule_refs": "host-rule"}
	config, err := ReadUCI(t.Context(), fake(values))
	if err != nil {
		t.Fatal(err)
	}
	got := config.Base.Policies[0].Rules[0].CIDRs
	if len(got) != 2 || got[0] != "195.82.146.214/32" || got[1] != "2001:db8::1/128" {
		t.Fatal(got)
	}
	values["resproxy.@rule[0].cidrs"] = "195.82.146.214/33"
	if _, err := ReadUCI(t.Context(), fake(values)); err == nil {
		t.Fatal("invalid mask accepted")
	}
}
