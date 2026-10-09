package settings

import (
	"context"
	"testing"
)

func TestNamedStandaloneRulesAndStableReferences(t *testing.T) {
	values := map[string]string{"resproxy.@rule[0].id": "stable-id", "resproxy.@rule[0].name": "Рабочие сайты", "resproxy.@rule[0].action": "direct", "resproxy.@rule[0].ports": "80,443", "resproxy.@rule[0].network": "tcp", "resproxy.@rule[0].protocols": "http tls", "resproxy.@rule[0].inbounds": "socks"}
	c, err := ReadUCI(context.Background(), fake(values))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.RuleGroups) != 1 || len(c.Base.Policies[0].Rules) != 0 {
		t.Fatal("standalone rule was implicitly attached")
	}
	values["resproxy.@policy[0].id"] = "main"
	values["resproxy.@policy[0].rule_refs"] = "stable-id"
	values["resproxy.@rule[0].name"] = "Новое название"
	c, err = ReadUCI(context.Background(), fake(values))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Base.Policies[0].Rules[0]
	if r.ID != "stable-id" || r.Name != "Новое название" || r.Ports != "80,443" || r.Network != "tcp" || len(r.Protocols) != 2 || len(r.Inbounds) != 1 {
		t.Fatalf("name or traffic fields lost: %+v", r)
	}
}
