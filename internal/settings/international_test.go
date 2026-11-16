package settings

import (
	"context"
	"testing"
)

func TestInternationalNamesAndDomainsKeepStableReferences(t *testing.T) {
	values := map[string]string{
		"resproxy.@rule[0].id": "stable-rule", "resproxy.@rule[0].name": "Работа 中国 🔒", "resproxy.@rule[0].action": "direct",
		"resproxy.@rule[0].domains": "FULL:ПРИМЕР.РФ EXAMPLE.COM",
		"resproxy.@policy[0].id":    "stable-route", "resproxy.@policy[0].name": "اشتراک خانه", "resproxy.@policy[0].rule_refs": "stable-rule",
		"resproxy.@subscription[0].id": "stable-source", "resproxy.@subscription[0].name": "我的订阅", "resproxy.@subscription[0].url": "https://example.com/feed",
	}
	config, err := ReadUCI(context.Background(), fake(values))
	if err != nil {
		t.Fatal(err)
	}
	rule := config.Base.Policies[0].Rules[0]
	if rule.ID != "stable-rule" || rule.Name != "Работа 中国 🔒" || rule.Domains[0] != "full:xn--e1afmkfd.xn--p1ai" || rule.Domains[1] != "example.com" || config.Policies[0].ID != "stable-route" || config.Subscriptions[0].ID != "stable-source" {
		t.Fatalf("international input changed identity or was not normalized: %+v", config)
	}
	values["resproxy.@rule[0].name"] = "Δίκτυο СМЕШАННЫЙ Регистр"
	values["resproxy.@policy[0].name"] = "Другое Имя"
	values["resproxy.@subscription[0].name"] = "اشتراك جديد"
	renamed, err := ReadUCI(context.Background(), fake(values))
	if err != nil || renamed.Base.Policies[0].Rules[0].ID != rule.ID || renamed.Policies[0].ID != config.Policies[0].ID || renamed.Subscriptions[0].ID != config.Subscriptions[0].ID {
		t.Fatal("renaming broke references", err)
	}
}
