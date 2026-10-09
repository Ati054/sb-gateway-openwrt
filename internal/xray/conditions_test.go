package xray

import (
	"reflect"
	"testing"
)

func TestTrafficConditionValidation(t *testing.T) {
	valid := RouteRule{ID: "custom", Name: "Рабочие сервисы", Action: "direct", Network: "tcp", Protocols: []string{"http", "tls"}, Inbounds: []string{"socks"}, Ports: "80,443,1000-2000"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"0", "65536", "2-1", "80,", "80 443", "80:90", "-1", "1-2-3"} {
		r := valid
		r.Ports = bad
		if r.Validate() == nil {
			t.Fatalf("accepted ports %q", bad)
		}
	}
	for _, change := range []func(*RouteRule){func(r *RouteRule) { r.Network = "icmp" }, func(r *RouteRule) { r.Protocols = []string{"unknown"} }, func(r *RouteRule) { r.Inbounds = []string{"resproxy-http"} }, func(r *RouteRule) { r.Name = "bad\nname" }} {
		r := valid
		change(&r)
		if r.Validate() == nil {
			t.Fatalf("accepted unsafe condition: %+v", r)
		}
	}
	if (RouteRule{ID: "empty", Action: "direct"}).Validate() == nil {
		t.Fatal("empty rule accepted")
	}
}
func TestConditionsRespectInboundScope(t *testing.T) {
	r := RouteRule{Inbounds: []string{"transparent"}, Network: "tcp", Protocols: []string{"tls"}, Ports: "443"}
	target := map[string]any{}
	if !applyRuleConditions(target, r, []string{"resproxy-transparent", "resproxy-transparent6", "resproxy-socks"}) {
		t.Fatal("missing condition")
	}
	if !reflect.DeepEqual(target["inboundTag"], []string{"resproxy-transparent", "resproxy-transparent6"}) || target["port"] != "443" {
		t.Fatal(target)
	}
	if applyRuleConditions(map[string]any{}, RouteRule{Inbounds: []string{"socks"}}, []string{"resproxy-transparent"}) {
		t.Fatal("SOCKS rule leaked into device route")
	}
}
func TestConditionalRulesRenderWithoutBroadeningGeoOrServiceRules(t *testing.T) {
	policy := BasePolicy{ID: "main", Rules: []RouteRule{
		{ID: "ports", Action: "direct", Ports: "443", Inbounds: []string{"socks"}},
		{ID: "missing-geo", Action: "direct", Country: "cn", UseGeosite: true, Ports: "80"},
		{ID: "missing-pack", Action: "direct", ServicePacks: []string{"cn-banks"}, Ports: "8080"},
		{ID: "domain-ip", Action: "direct", Domains: []string{"example.com"}, CIDRs: []string{"203.0.113.0/24"}, Protocols: []string{"tls"}},
	}}
	got := appendPolicyExceptions(nil, policy, []string{"resproxy-transparent", "resproxy-transparent6", "resproxy-socks"})
	if len(got) != 3 {
		t.Fatalf("not-ready data emitted a blanket rule: %+v", got)
	}
	if !reflect.DeepEqual(got[0].(map[string]any)["inboundTag"], []string{"resproxy-socks"}) || got[0].(map[string]any)["port"] != "443" {
		t.Fatal(got[0])
	}
	if got[1].(map[string]any)["domain"] == nil || got[1].(map[string]any)["ip"] != nil || got[2].(map[string]any)["domain"] != nil || got[2].(map[string]any)["ip"] == nil {
		t.Fatalf("domain/IP alternatives collapsed: %+v", got)
	}
	for _, r := range got {
		for _, in := range r.(map[string]any)["inboundTag"].([]string) {
			if in == "resproxy-http" || in == "resproxy-dns-main" {
				t.Fatal("internal traffic matched user rules")
			}
		}
	}
}

func TestCustomConditionKeepsRouteDeviceScope(t *testing.T) {
	policy := BasePolicy{ID: "device", SourceCIDRs: []string{"192.168.1.2/32"}, Rules: []RouteRule{{ID: "https", Name: "HTTPS", Action: "direct", Ports: "443"}}}
	got := appendPolicyExceptions(nil, policy, []string{"resproxy-transparent"})
	if len(got) != 1 || !reflect.DeepEqual(got[0].(map[string]any)["sourceIP"], policy.SourceCIDRs) {
		t.Fatalf("rule escaped route devices: %+v", got)
	}
}
