package dnsproxy

import (
	"fmt"
	"testing"
)

func TestDNSPoliciesReuseLeavesWithoutMergingActions(t *testing.T) {
	domains := make([]string, 512)
	for i := range domains {
		domains[i] = fmt.Sprintf("domain:d%d.shared.test", i)
	}
	policies := []Policy{
		{ID: "a", ProxyOutageAction: "block", Rules: []Rule{{Action: "direct", Domains: domains}, {Action: "proxy", Domains: []string{"domain:shared.test"}}}},
		{ID: "b", ProxyOutageAction: "wan", Rules: []Rule{{Action: "proxy", OutageAction: "wan", Domains: domains}, {Action: "direct", Domains: []string{"domain:shared.test"}}}},
	}
	matchers, err := compilePolicyMatchers(policies)
	if err != nil {
		t.Fatal(err)
	}
	a, b := matchers["a"], matchers["b"]
	if a.ordered[0].matcher.compact == nil || a.ordered[0].matcher.compact != b.ordered[0].matcher.compact {
		t.Fatal("identical immutable domain leaf was rebuilt for different actions")
	}
	if &a.ordered[0] == &b.ordered[0] || a.proxyWAN || !b.proxyWAN || b.ordered[0].outageAction != "wan" {
		t.Fatal("policy wrappers or outage actions were merged")
	}
	if !a.matches("d42.shared.test") || b.matches("d42.shared.test") || a.matches("other.shared.test") || !b.matches("other.shared.test") {
		t.Fatal("rule action or first-match order changed")
	}
	updated, err := compilePolicyMatchers(policies)
	if err != nil {
		t.Fatal(err)
	}
	if updated["a"].ordered[0].matcher.compact == a.ordered[0].matcher.compact {
		t.Fatal("a new generation reused the previous generation's domain leaf")
	}
}

func BenchmarkDNSLeafCompilation(b *testing.B) {
	domains := make([]string, 8192)
	for i := range domains {
		domains[i] = fmt.Sprintf("domain:d%d.shared.test", i)
	}
	policies := make([]Policy, 6)
	for i := range policies {
		action := "direct"
		if i%2 == 1 {
			action = "proxy"
		}
		// Every wrapper differs, so whole-policy sharing cannot help.
		policies[i] = Policy{ID: fmt.Sprint(i), Rules: []Rule{{Action: "proxy", Domains: []string{fmt.Sprintf("full:private%d.test", i)}}, {Action: action, Domains: domains}}}
	}
	for _, shared := range []bool{false, true} {
		name := "independent"
		if shared {
			name = "shared-leaves"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if shared {
					if _, err := compilePolicyMatchers(policies); err != nil {
						b.Fatal(err)
					}
				} else {
					for _, policy := range policies {
						if _, err := compileMatcher(policy); err != nil {
							b.Fatal(err)
						}
					}
				}
			}
		})
	}
}
