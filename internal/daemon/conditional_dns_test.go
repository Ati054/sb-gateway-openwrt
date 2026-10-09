package daemon

import (
	"example.org/resilient-proxy/internal/xray"
	"testing"
)

func TestApplicationConditionsDoNotBecomeUnconditionalDirectDNS(t *testing.T) {
	b := xray.DefaultBaseConfig()
	for _, r := range []xray.RouteRule{{Ports: "443"}, {Network: "tcp"}, {Protocols: []string{"tls"}}, {Inbounds: []string{"socks"}}} {
		r.ID = "conditional"
		r.Action = "direct"
		r.Domains = []string{"example.com"}
		b.Policies[0].Rules = []xray.RouteRule{r}
		if len(dnsPoliciesFromBase(b)[0].Rules) != 0 {
			t.Fatalf("conditional WAN rule became broad DNS: %+v", r)
		}
	}
	b.Policies[0].Rules = []xray.RouteRule{{ID: "plain", Action: "direct", Domains: []string{"example.com"}}}
	if len(dnsPoliciesFromBase(b)[0].Rules) != 1 {
		t.Fatal("ordinary domain DNS rule missing")
	}
}
