package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// One active-path lane plus up to ten independent emergency/background lanes.
// The configured batch size still determines how many lanes actually run.
const ProbeLanes = 11
const RefreshProbeTag = "resproxy-refresh-probe"

var policyIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type BaseConfig struct {
	APIPort           int
	TransparentPort   int
	HTTPPort          int
	SocksPort         int
	ProbeBudget       int
	ProbeBasePort     int
	DNSPolicyBasePort int
	Policies          []BasePolicy
	DefaultPolicy     string
	DomainStrategy    string // AsIs or IPOnDemand; routing DNS is always proxied.
	RoutingDNSURL     string
}

type BasePolicy struct {
	ID          string
	SourceCIDRs []string    // Optional transparent-client source selection.
	Rules       []RouteRule // Ordered reusable groups; first match wins.
}

type RouteRule struct {
	ID                string
	Name              string // User-facing label; ID remains the stable reference.
	Network           string // Empty means TCP and UDP.
	Protocols         []string
	Inbounds          []string // transparent and/or socks; empty means both.
	Ports             string   // Destination ports/ranges.
	Action            string   // direct or proxy
	OutageAction      string   // Legacy per-match outage setting.
	ProxyOutageAction string   // Rule-database choice for all proxy traffic of referencing lists.
	Domains           []string
	CIDRs             []string
	ServicePacks      []string // Optional reviewed domain catalog groups.
	Country           string   // ru, cn, ir, or empty
	UseGeosite        bool
	UseGeoIP          bool
	CountryDomains    []string // Compiled selected GeoSite entries.
	CountryGeoIP      string   // Immutable content-addressed Xray ext reference.
	CountryCIDRs      []string // Compiled selected GeoIP entries.
	ServiceDomains    []string // Compiled selected domain catalog entries.
}

func DefaultBaseConfig() BaseConfig {
	return BaseConfig{
		APIPort: 10085, TransparentPort: 12345, HTTPPort: 19080, SocksPort: 19081,
		ProbeBasePort: 19082, DNSPolicyBasePort: 19100,
		Policies: []BasePolicy{{ID: "main"}}, DefaultPolicy: "main",
		DomainStrategy: "AsIs", RoutingDNSURL: "https://1.1.1.1/dns-query",
	}
}

func (c BaseConfig) ProbeLaneCount() int {
	budget := c.ProbeBudget
	if budget <= 0 {
		budget = 10
	}
	return min(budget, 64) + 1
}

func (c BaseConfig) Validate() error {
	if c.ProbeBudget < 0 || c.ProbeBudget > 64 {
		return errors.New("invalid probe concurrency")
	}
	if c.DomainStrategy != "AsIs" && c.DomainStrategy != "IPOnDemand" {
		return errors.New("invalid Xray domain strategy")
	}
	if c.DomainStrategy == "IPOnDemand" {
		endpoint, err := url.Parse(c.RoutingDNSURL)
		if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Host == "" ||
			endpoint.Path == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return errors.New("invalid routing DoH endpoint")
		}
		if _, err := netip.ParseAddr(endpoint.Hostname()); err != nil {
			return errors.New("routing DoH must use an IP address to avoid bootstrap DNS loops")
		}
		if endpoint.Port() != "" {
			port, err := strconv.Atoi(endpoint.Port())
			if err != nil || port < 1 || port > 65535 {
				return errors.New("invalid routing DoH port")
			}
		}
	}
	if len(c.Policies) == 0 || len(c.Policies) > 8 {
		return errors.New("one to eight policies are required")
	}
	ports := map[int]bool{}
	allPorts := []int{c.APIPort, c.TransparentPort, c.HTTPPort, c.SocksPort}
	for lane := 0; lane <= c.ProbeLaneCount(); lane++ {
		allPorts = append(allPorts, c.ProbeBasePort+lane)
	}
	for index := range c.Policies {
		allPorts = append(allPorts, c.DNSPolicyBasePort+index)
	}
	for _, port := range allPorts {
		if port < 1024 || port > 65535 || ports[port] {
			return errors.New("invalid or overlapping Xray ports")
		}
		ports[port] = true
	}
	ids := map[string]bool{}
	for _, policy := range c.Policies {
		if !policyIDPattern.MatchString(policy.ID) || ids[policy.ID] {
			return errors.New("invalid or duplicate policy ID")
		}
		ids[policy.ID] = true
		for _, raw := range policy.SourceCIDRs {
			if _, err := netip.ParsePrefix(raw); err != nil {
				return fmt.Errorf("invalid source CIDR for policy %q", policy.ID)
			}
		}
		if _, err := policy.ProxyOutageBehavior(); err != nil {
			return err
		}
		for _, rule := range policy.Rules {
			if err := rule.Validate(); err != nil {
				return fmt.Errorf("policy %q: %w", policy.ID, err)
			}
		}

	}
	if !ids[c.DefaultPolicy] {
		return errors.New("default policy is not defined")
	}
	return nil
}

func (rule RouteRule) Validate() error {
	if err := rule.validateConditions(); err != nil {
		return err
	}
	if !policyIDPattern.MatchString(rule.ID) || (rule.Action != "direct" && rule.Action != "proxy") {
		return errors.New("invalid rule group ID or action")
	}
	if rule.ProxyOutageAction != "" && rule.ProxyOutageAction != "block" && rule.ProxyOutageAction != "wan" {
		return errors.New("invalid proxy traffic outage action")
	}
	if rule.OutageAction != "" && rule.OutageAction != "block" && rule.OutageAction != "wan" {
		return errors.New("invalid proxy outage action")
	}
	if rule.Country != "" && rule.Country != "ru" && rule.Country != "cn" && rule.Country != "ir" {
		return fmt.Errorf("invalid country in rule group %q", rule.ID)
	}
	if (rule.UseGeosite || rule.UseGeoIP) && rule.Country == "" {
		return fmt.Errorf("country is required in rule group %q", rule.ID)
	}
	if len(rule.Domains)+len(rule.CIDRs) > 4096 ||
		(len(rule.Domains)+len(rule.CIDRs)+len(rule.ServicePacks) == 0 && !rule.UseGeosite && !rule.UseGeoIP && !rule.hasConditions()) {
		return fmt.Errorf("empty or oversized rule group %q", rule.ID)
	}
	for _, raw := range rule.Domains {
		if !validRuleDomain(raw) {
			return fmt.Errorf("invalid domain in rule group %q", rule.ID)
		}
	}
	for _, raw := range rule.CountryDomains {
		if !validCountryDomain(raw) {
			return fmt.Errorf("invalid GeoSite domain in rule group %q", rule.ID)
		}
	}
	for _, raw := range rule.ServiceDomains {
		if !validCountryDomain(raw) {
			return fmt.Errorf("invalid catalog domain in rule group %q", rule.ID)
		}
	}
	for _, raw := range rule.CIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Bits() == 0 {
			return fmt.Errorf("invalid IP network in rule group %q", rule.ID)
		}
	}
	if rule.CountryGeoIP != "" {
		if _, ok := geoipasset.ReferenceName(rule.CountryGeoIP); !ok {
			return errors.New("invalid GeoIP reference")
		}
	}
	for _, raw := range rule.CountryCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Bits() == 0 {
			return fmt.Errorf("invalid GeoIP network in rule group %q", rule.ID)
		}
	}
	return nil
}

var domainLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func validDirectDomain(value string) bool {
	if value == "" || len(value) > 253 || value != strings.ToLower(value) || strings.Trim(value, ".") != value || !strings.Contains(value, ".") {
		return false
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !domainLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func validRuleDomain(value string) bool {
	if len(value) > 1032 {
		return false
	}
	if strings.HasPrefix(value, "regexp:") {
		_, err := regexp.Compile(strings.TrimPrefix(value, "regexp:"))
		return err == nil
	}
	for _, prefix := range []string{"domain:", "full:"} {
		if strings.HasPrefix(value, prefix) {
			name := strings.TrimPrefix(value, prefix)
			if prefix == "domain:" && domainLabelPattern.MatchString(name) {
				return true // Explicit top-level zone, e.g. domain:ru.
			}
			return validDirectDomain(name)
		}
	}
	if strings.HasPrefix(value, "keyword:") {
		key := strings.TrimPrefix(value, "keyword:")
		return key != "" && !strings.ContainsAny(key, "\r\n\x00")
	}
	return validDirectDomain(value)
}

func validCountryDomain(value string) bool {
	if _, ok := geoipasset.DomainReferenceName(value); ok {
		return true
	}

	if validRuleDomain(value) {
		return true
	}
	// Reviewed GeoSite country lists include top-level domains such as
	// domain:ru and domain:cn. Do not allow this shortcut in custom rules.
	if strings.HasPrefix(value, "domain:") {
		label := strings.TrimPrefix(value, "domain:")
		return domainLabelPattern.MatchString(label)
	}
	return false
}

// RenderBase starts Xray with fail-closed balancers. Live subscription updates
// add candidate outbounds via the API; this static config is not rewritten by
// a routine refresh. Port numbers and tags are stable across generations.
func RenderBase(c BaseConfig) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	apiTag := "resproxy-api"
	inbounds := []any{
		map[string]any{"tag": apiTag, "listen": "127.0.0.1", "port": c.APIPort,
			"protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"}},
		map[string]any{"tag": "resproxy-transparent", "listen": "127.0.0.1", "port": c.TransparentPort,
			"protocol": "dokodemo-door", "settings": map[string]any{"network": "tcp,udp", "followRedirect": true},
			"streamSettings": map[string]any{"sockopt": map[string]any{"tproxy": "tproxy"}},
			"sniffing":       map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true}},
		map[string]any{"tag": "resproxy-transparent6", "listen": "::1", "port": c.TransparentPort,
			"protocol": "dokodemo-door", "settings": map[string]any{"network": "tcp,udp", "followRedirect": true},
			"streamSettings": map[string]any{"sockopt": map[string]any{"tproxy": "tproxy", "V6Only": true}},
			"sniffing":       map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true}},
		map[string]any{"tag": "resproxy-socks", "listen": "127.0.0.1", "port": c.SocksPort,
			"protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true},
			"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true}},
		map[string]any{"tag": "resproxy-http", "listen": "127.0.0.1", "port": c.HTTPPort,
			"protocol": "http", "settings": map[string]any{"allowTransparent": false}},
	}
	dnsTags := make([]string, 0, len(c.Policies))
	for index, policy := range c.Policies {
		tag := "resproxy-dns-" + policy.ID
		dnsTags = append(dnsTags, tag)
		inbounds = append(inbounds, map[string]any{
			"tag": tag, "listen": "127.0.0.1", "port": c.DNSPolicyBasePort + index,
			"protocol": "http", "settings": map[string]any{"allowTransparent": false},
		})
	}
	rules := []any{map[string]any{"type": "field", "inboundTag": []string{apiTag}, "outboundTag": apiTag}}
	if c.DomainStrategy == "IPOnDemand" {
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"resproxy-routing-dns"},
			"balancerTag": "resproxy-" + c.DefaultPolicy})
	}
	balancers := make([]any, 0, len(c.Policies)+c.ProbeLaneCount())
	for _, policy := range c.Policies {
		balancers = append(balancers, map[string]any{
			"tag": "resproxy-" + policy.ID, "selector": []string{"block"},
			"strategy": map[string]any{"type": "random"},
		})
		if policy.HasWANFallback() {
			balancers = append(balancers, map[string]any{
				"tag": WANFallbackTag(policy.ID), "selector": []string{"block"},
				"strategy": map[string]any{"type": "random"},
			})
		}
		if len(policy.SourceCIDRs) > 0 {
			rules = appendPolicyExceptions(rules, policy, []string{"resproxy-transparent", "resproxy-transparent6"})
			rules = append(rules, map[string]any{
				"type": "field", "inboundTag": []string{"resproxy-transparent", "resproxy-transparent6"},
				"sourceIP": policy.SourceCIDRs, "balancerTag": policy.ProxyTrafficTag(),
			})
		}
	}
	probeTags := HealthProbeTags(c)
	for lane, tag := range probeTags {
		inbounds = append(inbounds, map[string]any{"tag": tag, "listen": "127.0.0.1", "port": c.ProbeBasePort + lane,
			"protocol": "http", "settings": map[string]any{"allowTransparent": false}})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{tag}, "balancerTag": tag})
		balancers = append(balancers, map[string]any{"tag": tag, "selector": []string{"block"},
			"strategy": map[string]any{"type": "random"}})
	}
	inbounds = append(inbounds, map[string]any{"tag": RefreshProbeTag, "listen": "127.0.0.1", "port": c.ProbeBasePort + c.ProbeLaneCount(),
		"protocol": "http", "settings": map[string]any{"allowTransparent": false}})
	rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{RefreshProbeTag}, "balancerTag": RefreshProbeTag})
	balancers = append(balancers, map[string]any{"tag": RefreshProbeTag, "selector": []string{"block"},
		"strategy": map[string]any{"type": "random"}})
	for _, policy := range c.Policies {
		if policy.ID == c.DefaultPolicy {
			fallback := policy
			fallback.SourceCIDRs = nil
			// The HTTP inbound is also used for subscription fetching. Never let
			// a user's direct-WAN group divert that internal proxy-only path.
			rules = appendPolicyExceptions(rules, fallback, []string{"resproxy-transparent", "resproxy-transparent6", "resproxy-socks"})
			break
		}
	}
	protectedInbounds := append([]string{"resproxy-transparent", "resproxy-transparent6", "resproxy-socks", "resproxy-http"}, dnsTags...)
	rules = append(rules,
		map[string]any{"type": "field", "inboundTag": protectedInbounds,
			"ip": []string{"127.0.0.0/8", "169.254.0.0/16", "::1/128", "fe80::/10"}, "outboundTag": "block"},
	)
	for _, policy := range c.Policies {
		rules = append(rules, map[string]any{"type": "field",
			"inboundTag":  []string{"resproxy-dns-" + policy.ID},
			"balancerTag": "resproxy-" + policy.ID})
	}
	rules = append(rules, map[string]any{"type": "field",
		"inboundTag": []string{"resproxy-http"}, "balancerTag": "resproxy-" + c.DefaultPolicy})
	rules = append(rules, map[string]any{"type": "field",
		"inboundTag":  []string{"resproxy-transparent", "resproxy-transparent6", "resproxy-socks"},
		"balancerTag": c.defaultProxyTrafficTag()})
	config := map[string]any{
		"policy":   map[string]any{"levels": map[string]any{"0": map[string]any{"connIdle": 120, "uplinkOnly": 2, "downlinkOnly": 5}}},
		"log":      map[string]any{"access": "none", "loglevel": "warning"},
		"api":      map[string]any{"tag": apiTag, "services": []string{"RoutingService", "HandlerService", "StatsService"}},
		"inbounds": inbounds,
		"outbounds": []any{
			map[string]any{"tag": "block", "protocol": "blackhole", "settings": map[string]any{}},
			map[string]any{"tag": "direct-wan", "protocol": "freedom", "settings": map[string]any{}},
		},
		"stats":   map[string]any{},
		"routing": map[string]any{"domainStrategy": c.DomainStrategy, "rules": rules, "balancers": balancers},
	}
	if c.DomainStrategy == "IPOnDemand" {
		config["dns"] = map[string]any{"servers": []string{c.RoutingDNSURL},
			"tag": "resproxy-routing-dns", "queryStrategy": "UseIPv4"}
	}
	return json.MarshalIndent(config, "", "  ")
}

func WANFallbackTag(policyID string) string { return "resproxy-rule-wan:" + policyID }

// All explicitly configured database rules used by one list must agree.
// Unconfigured older rules inherit the list's database choice.
func (policy BasePolicy) ProxyOutageBehavior() (string, error) {
	behavior := ""
	for _, rule := range policy.Rules {
		if rule.ProxyOutageAction == "" {
			continue
		}
		if rule.ProxyOutageAction != "block" && rule.ProxyOutageAction != "wan" {
			return "", fmt.Errorf("policy %q: invalid proxy outage action", policy.ID)
		}
		if behavior != "" && behavior != rule.ProxyOutageAction {
			return "", fmt.Errorf("policy %q: conflicting proxy outage choices in referenced rules", policy.ID)
		}
		behavior = rule.ProxyOutageAction
	}
	if behavior == "" {
		behavior = "block"
	}
	return behavior, nil
}

func (policy BasePolicy) ProxyTrafficTag() string {
	behavior, _ := policy.ProxyOutageBehavior()
	if behavior == "wan" {
		return WANFallbackTag(policy.ID)
	}
	return "resproxy-" + policy.ID
}

func (c BaseConfig) defaultProxyTrafficTag() string {
	for _, policy := range c.Policies {
		if policy.ID == c.DefaultPolicy {
			return policy.ProxyTrafficTag()
		}
	}
	return "resproxy-" + c.DefaultPolicy
}

func (policy BasePolicy) HasWANFallback() bool {
	if behavior, _ := policy.ProxyOutageBehavior(); behavior == "wan" {
		return true
	}
	for _, rule := range policy.Rules {
		if rule.Action == "proxy" && rule.OutageAction == "wan" {
			return true
		}
	}
	return false
}

func HealthProbeTags(configs ...BaseConfig) []string {
	count := ProbeLanes
	if len(configs) > 0 {
		count = configs[0].ProbeLaneCount()
	}
	tags := []string{"outbound-health-probe", "outbound-health-background"}
	for lane := 2; lane < count; lane++ {
		tags = append(tags, fmt.Sprintf("outbound-health-background-%d", lane))
	}
	return tags
}

func appendPolicyExceptions(rules []any, policy BasePolicy, inbounds []string) []any {
	base := map[string]any{"type": "field", "inboundTag": inbounds, "outboundTag": "direct-wan"}
	if len(policy.SourceCIDRs) > 0 {
		base["sourceIP"] = policy.SourceCIDRs
	}
	for _, group := range policy.Rules {
		groupBase := make(map[string]any, len(base)+1)
		for name, value := range base {
			groupBase[name] = value
		}
		if !applyRuleConditions(groupBase, group, inbounds) {
			continue
		}
		delete(groupBase, "outboundTag")
		if group.Action == "direct" {
			groupBase["outboundTag"] = "direct-wan"
		} else {
			groupBase["balancerTag"] = policy.ProxyTrafficTag()
			if group.OutageAction == "wan" {
				groupBase["balancerTag"] = WANFallbackTag(policy.ID)
			}
		}
		if len(group.Domains)+len(group.CountryDomains)+len(group.ServiceDomains) > 0 {
			match := make(map[string]any, len(groupBase)+1)
			for name, value := range groupBase {
				match[name] = value
			}
			values := make([]string, 0, len(group.Domains)+len(group.CountryDomains)+len(group.ServiceDomains))
			for _, value := range group.Domains {
				if !strings.Contains(value, ":") {
					value = "domain:" + value
				}
				values = append(values, value)
			}
			values = append(values, group.CountryDomains...)
			values = append(values, group.ServiceDomains...)
			match["domain"] = values
			rules = append(rules, match)
		}
		if group.CountryGeoIP != "" {
			match := make(map[string]any, len(groupBase)+1)
			for name, value := range groupBase {
				match[name] = value
			}
			match["ip"] = []string{group.CountryGeoIP}
			rules = append(rules, match)
		}
		if len(group.CIDRs)+len(group.CountryCIDRs) > 0 {
			for _, is4 := range []bool{true, false} {
				values := []string{}
				for _, value := range append(append([]string{}, group.CIDRs...), group.CountryCIDRs...) {
					if netip.MustParsePrefix(value).Addr().Is4() == is4 {
						values = append(values, value)
					}
				}
				if len(values) > 0 {
					match := make(map[string]any, len(groupBase)+1)
					for name, value := range groupBase {
						match[name] = value
					}
					match["ip"] = values
					rules = append(rules, match)
				}
			}
		}
		if len(group.Domains)+len(group.CountryDomains)+len(group.ServiceDomains)+len(group.CIDRs)+len(group.CountryCIDRs) == 0 && !group.UseGeosite && !group.UseGeoIP && len(group.ServicePacks) == 0 {
			rules = append(rules, groupBase)
		}

	}
	return rules
}
