package xray

import (
	"encoding/json"
	"testing"
)

func TestRenderBaseHasOneCoreAndFailClosedRoutes(t *testing.T) {
	config := DefaultBaseConfig()
	config.Policies = []BasePolicy{{ID: "family", SourceCIDRs: []string{"192.168.10.0/24"}}, {ID: "office"}}
	config.DefaultPolicy = "office"
	body, err := RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["log"].(map[string]any)["access"] != "none" {
		t.Fatal("routine Xray access probes must not flood the system journal")
	}
	inbounds := parsed["inbounds"].([]any)
	if len(inbounds) != 6+ProbeLanes+len(config.Policies) {
		t.Fatalf("unexpected inbound count: %d", len(inbounds))
	}
	routing := parsed["routing"].(map[string]any)
	balancers := routing["balancers"].([]any)
	if len(balancers) != 3+ProbeLanes {
		t.Fatalf("unexpected balancer count: %d", len(balancers))
	}
	for _, raw := range balancers {
		members := raw.(map[string]any)["selector"].([]any)
		if len(members) != 1 || members[0] != "block" {
			t.Fatalf("balancer is not fail-closed: %#v", raw)
		}
	}
}

func TestDomainStrategyUsesProxiedRoutingDNSOnlyWhenEnabled(t *testing.T) {
	config := DefaultBaseConfig()
	config.DomainStrategy = "IPOnDemand"
	body, err := RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["routing"].(map[string]any)["domainStrategy"] != "IPOnDemand" {
		t.Fatal("domain strategy was not rendered")
	}
	if parsed["dns"].(map[string]any)["tag"] != "resproxy-routing-dns" {
		t.Fatal("routing DNS has no dedicated inbound tag")
	}
	rules := parsed["routing"].(map[string]any)["rules"].([]any)
	if !containsRuleValue(rules[1].(map[string]any)["inboundTag"], "resproxy-routing-dns") ||
		rules[1].(map[string]any)["balancerTag"] != "resproxy-main" {
		t.Fatal("routing DNS could bypass the default proxy policy")
	}
	config.DomainStrategy = "AsIs"
	body, err = RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	parsed = nil
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["dns"] != nil {
		t.Fatal("AsIs should not start a second DNS client")
	}
}

func TestDomainStrategyRejectsUnsafeResolver(t *testing.T) {
	config := DefaultBaseConfig()
	config.DomainStrategy = "IPIfNonMatch"
	if _, err := RenderBase(config); err == nil {
		t.Fatal("IPIfNonMatch cannot work with a catch-all policy rule")
	}
	config.DomainStrategy = "IPOnDemand"
	for _, endpoint := range []string{"http://1.1.1.1/dns-query", "https://dns.example/dns-query", "https://u:p@1.1.1.1/dns-query"} {
		config.RoutingDNSURL = endpoint
		if _, err := RenderBase(config); err == nil {
			t.Fatalf("unsafe routing DNS accepted: %s", endpoint)
		}
	}
}

func TestBaseConfigRejectsPortCollisionAndBadSource(t *testing.T) {
	config := DefaultBaseConfig()
	config.ProbeBasePort = config.HTTPPort
	if _, err := RenderBase(config); err == nil {
		t.Fatal("overlapping ports accepted")
	}
	config = DefaultBaseConfig()
	config.DNSPolicyBasePort = config.ProbeBasePort
	if _, err := RenderBase(config); err == nil {
		t.Fatal("DNS policy port overlap accepted")
	}
	config = DefaultBaseConfig()
	config.Policies[0].SourceCIDRs = []string{"not-an-ip"}
	if _, err := RenderBase(config); err == nil {
		t.Fatal("invalid source CIDR accepted")
	}
}

func TestRenderBaseRoutesDNSByPolicy(t *testing.T) {
	config := DefaultBaseConfig()
	config.Policies = []BasePolicy{{ID: "family"}, {ID: "main"}}
	body, err := RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Inbounds []struct {
			Tag  string `json:"tag"`
			Port int    `json:"port"`
		} `json:"inbounds"`
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	for index, policy := range config.Policies {
		tag := "resproxy-dns-" + policy.ID
		foundPort, foundRule := false, false
		for _, inbound := range parsed.Inbounds {
			if inbound.Tag == tag && inbound.Port == config.DNSPolicyBasePort+index {
				foundPort = true
			}
		}
		for _, rule := range parsed.Routing.Rules {
			if containsRuleValue(rule["inboundTag"], tag) && rule["balancerTag"] == "resproxy-"+policy.ID {
				foundRule = true
			}
		}
		if !foundPort || !foundRule {
			t.Fatalf("DNS policy %s has no isolated inbound and route", policy.ID)
		}
	}
}

func TestRenderBaseHasTenIndependentEmergencyProbeLanes(t *testing.T) {
	config := DefaultBaseConfig()
	body, err := RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Inbounds []struct {
			Tag  string `json:"tag"`
			Port int    `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	ports := make(map[string]int, len(parsed.Inbounds))
	for _, inbound := range parsed.Inbounds {
		ports[inbound.Tag] = inbound.Port
	}
	if ProbeLanes != 11 || len(HealthProbeTags()) != 11 {
		t.Fatalf("configured emergency batch 10 has only %d probe lanes", ProbeLanes-1)
	}
	for lane, tag := range HealthProbeTags() {
		if ports[tag] != config.ProbeBasePort+lane {
			t.Fatalf("lane %d has wrong port: %s=%d", lane, tag, ports[tag])
		}
	}
	if ports[RefreshProbeTag] != config.ProbeBasePort+ProbeLanes {
		t.Fatalf("refresh probe port collision: %d", ports[RefreshProbeTag])
	}
}

func TestRenderBaseDirectExceptionsPrecedePolicyRoute(t *testing.T) {
	config := DefaultBaseConfig()
	config.Policies = []BasePolicy{
		{ID: "family", SourceCIDRs: []string{"192.168.10.2/32"}, Rules: []RouteRule{
			{ID: "torrent", Name: "BitTorrent", Action: "direct", Protocols: []string{"bittorrent"}},
			{ID: "example", Action: "direct", Domains: []string{"example.com"}, CIDRs: []string{"203.0.113.0/24"}},
		}},
		{ID: "main", Rules: []RouteRule{{ID: "zones", Action: "direct", Country: "ru", UseGeosite: true, CountryDomains: []string{"domain:ru", "domain:xn--p1ai"}}}},
	}
	config.DefaultPolicy = "main"
	body, err := RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	familyRule, domainRule, torrentRule, ipRule, ruRule := -1, -1, -1, -1, -1
	for i, rule := range parsed.Routing.Rules {
		if rule["balancerTag"] == "resproxy-family" &&
			containsRuleValue(rule["inboundTag"], "resproxy-transparent") {
			familyRule = i
		}
		if rule["outboundTag"] != "direct-wan" {
			continue
		}
		if containsRuleValue(rule["domain"], "domain:example.com") {
			domainRule = i
		}
		if containsRuleValue(rule["protocol"], "bittorrent") {
			torrentRule = i
		}
		if containsRuleValue(rule["ip"], "203.0.113.0/24") {
			ipRule = i
		}
		if containsRuleValue(rule["domain"], "domain:ru") && containsRuleValue(rule["domain"], "domain:xn--p1ai") {
			ruRule = i
		}
	}
	if familyRule < 0 || domainRule < 0 || torrentRule < 0 || ipRule < 0 || ruRule < 0 ||
		domainRule >= familyRule || torrentRule >= familyRule || ipRule >= familyRule || ruRule <= familyRule {
		t.Fatalf("direct rule order is unsafe: family=%d domain=%d torrent=%d ip=%d ru=%d", familyRule, domainRule, torrentRule, ipRule, ruRule)
	}
	for _, i := range []int{domainRule, torrentRule, ipRule} {
		if !containsRuleValue(parsed.Routing.Rules[i]["sourceIP"], "192.168.10.2/32") {
			t.Fatalf("family exception escaped device scope: %#v", parsed.Routing.Rules[i])
		}
	}
	for _, rule := range parsed.Routing.Rules {
		if rule["outboundTag"] == "direct-wan" && containsRuleValue(rule["inboundTag"], "resproxy-http") {
			t.Fatal("internal HTTP proxy path could be diverted to WAN")
		}
	}
	if parsed.Routing.Rules[ruRule]["sourceIP"] != nil {
		t.Fatal("default exception must apply to unmatched clients")
	}
}

func containsRuleValue(raw any, expected string) bool {
	values, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestRenderCountryRuleUsesBothIndependentMatches(t *testing.T) {
	config := DefaultBaseConfig()
	config.Policies[0].Rules = []RouteRule{{ID: "country-ru", Action: "direct", Country: "ru",
		UseGeosite: true, UseGeoIP: true,
		CountryDomains: []string{"domain:ru", "domain:example.ru"},
		CountryCIDRs:   []string{"203.0.113.0/24"}}}
	body, err := RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	domain, ip := false, false
	for _, rule := range parsed.Routing.Rules {
		if rule["outboundTag"] != "direct-wan" {
			continue
		}
		domain = domain || containsRuleValue(rule["domain"], "domain:ru")
		ip = ip || containsRuleValue(rule["ip"], "203.0.113.0/24")
	}
	if !domain || !ip {
		t.Fatalf("country domain/IP alternatives missing: %s", body)
	}
}

func TestDirectExceptionsRejectUnsafeValues(t *testing.T) {
	for _, change := range []func(*BasePolicy){
		func(p *BasePolicy) {
			p.Rules = []RouteRule{{ID: "bad", Action: "direct", Domains: []string{"*.example.com"}}}
		},
		func(p *BasePolicy) {
			p.Rules = []RouteRule{{ID: "bad", Action: "direct", Domains: []string{"example.com;bad"}}}
		},
		func(p *BasePolicy) {
			p.Rules = []RouteRule{{ID: "bad", Action: "direct", CIDRs: []string{"0.0.0.0/0"}}}
		},
		func(p *BasePolicy) {
			p.Rules = []RouteRule{{ID: "bad", Action: "direct", CIDRs: []string{"not-an-ip"}}}
		},
	} {
		config := DefaultBaseConfig()
		change(&config.Policies[0])
		if _, err := RenderBase(config); err == nil {
			t.Fatal("invalid direct exception accepted")
		}
	}
}

func TestReusableRulesKeepPriorityAndUseORForDomainAndIP(t *testing.T) {
	config := DefaultBaseConfig()
	config.Policies[0].Rules = []RouteRule{
		{ID: "force-proxy", Action: "proxy", Domains: []string{"full:login.example.com"}},
		{ID: "country-custom", Action: "direct", Domains: []string{"example.com"}, CIDRs: []string{"203.0.113.0/24"}},
	}
	body, err := RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	proxyIndex, domainIndex, ipIndex := -1, -1, -1
	for index, rule := range parsed.Routing.Rules {
		if containsRuleValue(rule["domain"], "full:login.example.com") {
			proxyIndex = index
			if rule["balancerTag"] != "resproxy-main" || rule["ip"] != nil {
				t.Fatalf("proxy exception is not isolated: %#v", rule)
			}
		}
		if containsRuleValue(rule["domain"], "domain:example.com") {
			domainIndex = index
			if rule["outboundTag"] != "direct-wan" || rule["ip"] != nil {
				t.Fatalf("domain and IP were incorrectly combined with AND: %#v", rule)
			}
		}
		if containsRuleValue(rule["ip"], "203.0.113.0/24") {
			ipIndex = index
			if rule["outboundTag"] != "direct-wan" || rule["domain"] != nil {
				t.Fatalf("IP and domain were incorrectly combined with AND: %#v", rule)
			}
		}
	}
	if proxyIndex < 0 || domainIndex <= proxyIndex || ipIndex <= domainIndex {
		t.Fatalf("rule group priority lost: proxy=%d domain=%d ip=%d", proxyIndex, domainIndex, ipIndex)
	}
}
