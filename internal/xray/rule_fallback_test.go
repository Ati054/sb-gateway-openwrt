package xray

import (
	"encoding/json"
	"testing"
)

func TestRuleFallbackRoutingIsScopedOrderedAndInitiallyBlocked(t *testing.T) {
	c := DefaultBaseConfig()
	c.Policies = []BasePolicy{{ID: "pc", SourceCIDRs: []string{"192.0.2.8/32"}, Rules: []RouteRule{
		{ID: "strict", Action: "proxy", Domains: []string{"full:private.example"}},
		{ID: "fallback", Action: "proxy", OutageAction: "wan", Domains: []string{"example.com"}, CIDRs: []string{"198.51.100.0/24"}, Network: "tcp", Ports: "443"},
		{ID: "direct", Action: "direct", OutageAction: "wan", Domains: []string{"direct.example"}},
	}}, {ID: "main"}, {ID: "pc-fallback-wan"}}
	body, err := RenderBase(c)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err = json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	routing := d["routing"].(map[string]any)
	count := 0
	for _, raw := range routing["balancers"].([]any) {
		b := raw.(map[string]any)
		if b["tag"] == WANFallbackTag("pc") {
			count++
			if b["selector"].([]any)[0] != "block" {
				t.Fatal("fallback opened before health proof")
			}
		}
	}
	if count != 1 {
		t.Fatalf("fallback balancers=%d", count)
	}
	rules := routing["rules"].([]any)
	scoped := []map[string]any{}
	for _, raw := range rules {
		r := raw.(map[string]any)
		if r["sourceIP"] != nil {
			scoped = append(scoped, r)
		}
	}
	if len(scoped) != 5 || scoped[0]["balancerTag"] != "resproxy-pc" || scoped[1]["balancerTag"] != WANFallbackTag("pc") || scoped[2]["balancerTag"] != WANFallbackTag("pc") || scoped[3]["outboundTag"] != "direct-wan" || scoped[4]["balancerTag"] != "resproxy-pc" {
		t.Fatalf("rules lost order/scope: %#v", scoped)
	}
	if scoped[1]["network"] != "tcp" || scoped[1]["port"] != "443" {
		t.Fatal("fallback dropped conditions")
	}
	if WANFallbackTag("pc") == "resproxy-pc-fallback-wan" {
		t.Fatal("fallback tag can collide with route-list ID")
	}
	tags := balancerTags(c)
	found := false
	for _, tag := range tags {
		found = found || tag == WANFallbackTag("pc")
	}
	if !found {
		t.Fatal("hot routing does not preserve auxiliary selection")
	}
}

func TestRuleFallbackRejectsUnknownActions(t *testing.T) {
	r := RouteRule{ID: "test", Action: "proxy", Domains: []string{"example.com"}, OutageAction: "automatic"}
	if r.Validate() == nil {
		t.Fatal("unknown outage action accepted")
	}
}
