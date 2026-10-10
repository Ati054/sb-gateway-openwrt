package settings

import (
	"context"
	"testing"
)

func TestDatabaseOutageIndependentOfCardAction(t *testing.T) {
	for _, action := range []string{"direct", "proxy"} {
		for _, outage := range []string{"block", "wan"} {
			c, err := ReadUCI(context.Background(), fake(map[string]string{"resproxy.@rule[0].id": "cards", "resproxy.@rule[0].action": action, "resproxy.@rule[0].domains": "example.com", "resproxy.@rule[0].proxy_outage_action": outage, "resproxy.@policy[0].id": "pc", "resproxy.@policy[0].rule_refs": "cards", "resproxy.@policy[1].id": "other"}))
			if err != nil {
				t.Fatal(err)
			}
			if c.Policies[0].WANFallback != (outage == "wan") || c.Policies[1].WANFallback || c.Base.Policies[0].Rules[0].Action != action {
				t.Fatalf("action %s outage %s: %+v", action, outage, c.Policies)
			}
		}
	}
}
func TestDatabaseOutageConflictsRejected(t *testing.T) {
	_, err := ReadUCI(context.Background(), fake(map[string]string{"resproxy.@rule[0].id": "a", "resproxy.@rule[0].action": "direct", "resproxy.@rule[0].domains": "a.test", "resproxy.@rule[0].proxy_outage_action": "wan", "resproxy.@rule[1].id": "b", "resproxy.@rule[1].action": "proxy", "resproxy.@rule[1].domains": "b.test", "resproxy.@rule[1].proxy_outage_action": "block", "resproxy.@policy[0].id": "pc", "resproxy.@policy[0].rule_refs": "a b"}))
	if err == nil {
		t.Fatal("conflicting behavior silently accepted")
	}
}
