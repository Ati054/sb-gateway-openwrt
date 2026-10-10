package daemon

import (
	"example.org/resilient-proxy/internal/xray"
	"testing"
)

func TestDatabaseOutageDNSInheritedFromConditionalCards(t *testing.T) {
	policies := dnsPoliciesFromBase(xray.BaseConfig{Policies: []xray.BasePolicy{{ID: "pc", Rules: []xray.RouteRule{{ID: "torrent", Action: "direct", Protocols: []string{"bittorrent"}, ProxyOutageAction: "wan"}}}}})
	if len(policies) != 1 || policies[0].ProxyOutageAction != "wan" || len(policies[0].Rules) != 0 {
		t.Fatalf("conditional card lost independent DNS choice: %+v", policies)
	}
}
