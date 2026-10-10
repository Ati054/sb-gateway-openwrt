package xray

import (
	"encoding/json"
	"testing"
)

func TestDatabaseOutageRoutesUnmatchedTrafficAndPreservesCardAction(t *testing.T) {
	for _, action := range []string{"direct", "proxy"} {
		for _, behavior := range []string{"block", "wan"} {
			c := DefaultBaseConfig()
			c.DefaultPolicy = "all"
			c.Policies = []BasePolicy{{ID: "pc", SourceCIDRs: []string{"192.0.2.8/32"}, Rules: []RouteRule{{ID: "cards", Action: action, ProxyOutageAction: behavior, Domains: []string{"example.com"}}}}, {ID: "all", Rules: []RouteRule{{ID: "allcards", Action: action, ProxyOutageAction: behavior, Domains: []string{"example.net"}}}}, {ID: "other"}}
			body, err := RenderBase(c)
			if err != nil {
				t.Fatal(err)
			}
			var d map[string]any
			json.Unmarshal(body, &d)
			rules := d["routing"].(map[string]any)["rules"].([]any)
			var deviceCatch, card, httpCatch, dnsCatch, defaultCatch map[string]any
			for _, raw := range rules {
				r := raw.(map[string]any)
				in := r["inboundTag"].([]any)
				if r["sourceIP"] != nil {
					if r["domain"] != nil {
						card = r
					} else {
						deviceCatch = r
					}
				}
				if len(in) == 1 && in[0] == "resproxy-http" {
					httpCatch = r
				}
				if len(in) == 1 && in[0] == "resproxy-dns-all" {
					dnsCatch = r
				}
				if len(in) == 3 && in[2] == "resproxy-socks" && r["domain"] == nil && r["ip"] == nil {
					defaultCatch = r
				}
			}
			target := "resproxy-pc"
			allTarget := "resproxy-all"
			if behavior == "wan" {
				target = WANFallbackTag("pc")
				allTarget = WANFallbackTag("all")
			}
			if deviceCatch["balancerTag"] != target || defaultCatch["balancerTag"] != allTarget || httpCatch["balancerTag"] != "resproxy-all" || dnsCatch["balancerTag"] != "resproxy-all" {
				t.Fatalf("wrong proxy scope: %s %s %v %v %v %v", action, behavior, deviceCatch, defaultCatch, httpCatch, dnsCatch)
			}
			if action == "direct" && card["outboundTag"] != "direct-wan" || action == "proxy" && card["balancerTag"] != target {
				t.Fatal("card action changed")
			}
			if c.Policies[2].HasWANFallback() {
				t.Fatal("outage leaked into another list")
			}
		}
	}
}
