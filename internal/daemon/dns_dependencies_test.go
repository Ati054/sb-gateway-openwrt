package daemon

import (
	"example.org/resilient-proxy/internal/xray"
	"slices"
	"testing"
)

func TestPackDependenciesKeepChosenDNSActionAndPolicy(t *testing.T) {
	base := xray.DefaultBaseConfig()
	base.Policies = []xray.BasePolicy{{ID: "pc", SourceCIDRs: []string{"192.0.2.1/32"}, Rules: []xray.RouteRule{{ID: "exchange", Action: "proxy", ServiceDomains: []string{"domain:api.manepa.jp"}}, {ID: "direct", Action: "direct", CountryDomains: []string{"domain:example.ru"}}}}}
	got := dnsPoliciesFromBase(base)
	if len(got) != 1 || got[0].ID != "pc" || len(got[0].Rules) != 2 || got[0].Rules[0].Action != "proxy" || !slices.Contains(got[0].Rules[0].Domains, "domain:api.manepa.jp") || got[0].Rules[1].Action != "direct" {
		t.Fatal("DNS dependencies lost routing action", got)
	}
}
