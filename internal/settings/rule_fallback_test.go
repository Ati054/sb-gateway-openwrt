package settings

import (
	"context"
	"testing"
)

func TestReadUCIDerivesFallbackOnlyFromReferencedProxyRules(t *testing.T) {
	for _, action := range []string{"proxy", "direct"} {
		for _, outage := range []string{"", "block", "wan"} {
			c, err := ReadUCI(context.Background(), fake(map[string]string{
				"resproxy.@rule[0].id": "custom", "resproxy.@rule[0].action": action, "resproxy.@rule[0].domains": "example.com", "resproxy.@rule[0].outage_action": outage,
				"resproxy.@policy[0].id": "pc", "resproxy.@policy[0].rule_refs": "custom", "resproxy.@policy[1].id": "main",
			}))
			if err != nil {
				t.Fatal(err)
			}
			if c.RuleGroups[0].OutageAction != outage || c.Policies[0].WANFallback != (action == "proxy" && outage == "wan") || c.Policies[1].WANFallback {
				t.Fatalf("action=%s outage=%s derived=%+v", action, outage, c.Policies)
			}
		}
	}
}

func TestLegacyGlobalDNSFallbackIsReplacedByRulePolicy(t *testing.T) {
	for _, mode := range []string{"proxy-fallback-wan", "proxy-only"} {
		config, err := ReadUCI(context.Background(), fake(map[string]string{"resproxy.main.dns_mode": mode}))
		if err != nil || config.DNSMode != "proxy-only" {
			t.Fatalf("legacy DNS mode can bypass rules: %q %+v %v", mode, config, err)
		}
	}
}
