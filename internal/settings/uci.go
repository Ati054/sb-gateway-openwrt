package settings

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/Ati054/sb-gateway-openwrt/internal/capture"
	"github.com/Ati054/sb-gateway-openwrt/internal/dnsproxy"
	"github.com/Ati054/sb-gateway-openwrt/internal/health"
	"github.com/Ati054/sb-gateway-openwrt/internal/rulesets"
	"github.com/Ati054/sb-gateway-openwrt/internal/selection"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

type Getter func(context.Context, string) (string, error)

type Config struct {
	Enabled              bool
	RouteEnabled         bool
	LANInterface         string
	Capture              capture.Policy
	SubscriptionURL      string
	ProviderID           string
	UpdateMode           string
	UpdateSeconds        int
	MaxNodes             int
	RulesetUpdateMode    string
	RulesetUpdateSeconds int
	DNSMode              string
	DNSHandoffConfirmed  bool
	DNSProxyURL          string
	DNSDirectURL         string
	DNSPort              int
	Subscriptions        []Subscription
	RuleGroups           []xray.RouteRule
	Base                 xray.BaseConfig
	Policies             []Policy
}

type Subscription struct {
	UpdateTime    string
	ID            string
	URL           string
	UpdateMode    string
	UpdateSeconds int
	MaxNodes      int
	FetchVia      string
}

type Policy struct {
	ID             string
	Mode           string
	SelectionOrder []string
	NodeFilter     selection.Filter
	RuleRefs       []string
	WANFallback    bool // Derived from referenced rules; no route-list setting.
	SourceCIDRs    []string
	Health         health.Policy
}

func UCIGet(ctx context.Context, key string) (string, error) {
	output, err := exec.CommandContext(ctx, "/sbin/uci", "-q", "get", key).Output()
	return strings.TrimSpace(string(output)), err
}

// ReadUCI must never publish a partially read configuration after cancellation.
func ReadUCI(ctx context.Context, get Getter) (Config, error) {
	if get == nil {
		return Config{}, errors.New("UCI getter is required")
	}
	var interrupted error
	config, err := readUCI(ctx, func(ctx context.Context, key string) (string, error) {
		value, readErr := get(ctx, key)
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			interrupted = readErr
		}
		return value, readErr
	})
	if ctx.Err() != nil {
		return Config{}, fmt.Errorf("read UCI configuration: %w", ctx.Err())
	}
	if interrupted != nil {
		return Config{}, fmt.Errorf("read UCI configuration: %w", interrupted)
	}
	return config, err
}

func readUCI(ctx context.Context, get Getter) (Config, error) {
	if get == nil {
		return Config{}, errors.New("UCI getter is required")
	}
	read := func(key string) string {
		value, _ := get(ctx, "resproxy.main."+key)
		return strings.TrimSpace(value)
	}
	config := Config{
		Enabled: read("enabled") == "1", SubscriptionURL: read("subscription_url"),
		RouteEnabled: read("route_enabled") == "1",
		ProviderID:   read("provider_id"), UpdateMode: read("update_mode"), UpdateSeconds: 1800, MaxNodes: 1000,
		Base: xray.DefaultBaseConfig(),
	}
	if config.ProviderID == "" {
		config.ProviderID = "primary"
	}
	if config.UpdateMode == "" {
		config.UpdateMode = "automatic"
	}
	if config.UpdateMode != "automatic" && config.UpdateMode != "manual" {
		return Config{}, errors.New("update mode must be automatic or manual")
	}
	// Use OpenWrt's LAN configuration; this package has no separate device choice.
	if config.RouteEnabled {
		device, err := get(ctx, "network.lan.device")
		if err != nil {
			return Config{}, fmt.Errorf("read OpenWrt LAN device: %w", err)
		}
		config.LANInterface = strings.TrimSpace(device)
		if !regexp.MustCompile("^[A-Za-z0-9_.:-]{1,15}$").MatchString(config.LANInterface) {
			return Config{}, errors.New("invalid OpenWrt LAN device")
		}
	}
	var err error
	config.Capture, err = capture.New(read("ipv6_mode"), strings.Fields(read("bypass_cidrs")))
	if err != nil {
		return Config{}, fmt.Errorf("capture settings: %w", err)
	}
	config.Capture.BlockPing = read("block_ping") == "1"
	if config.UpdateSeconds, err = integer(read("update_interval_seconds"), 1800, 300, 86400); err != nil {
		return Config{}, fmt.Errorf("update interval: %w", err)
	}
	if config.MaxNodes, err = integer(read("max_nodes"), 1000, 1, 5000); err != nil {
		return Config{}, fmt.Errorf("node limit: %w", err)
	}
	// Rules are always refreshed automatically. Legacy manual UCI values must
	// not silently leave selected routing catalogs stale after an upgrade.
	config.RulesetUpdateMode = "automatic"
	if config.RulesetUpdateSeconds, err = integer(read("ruleset_update_interval_seconds"), 86400, 3600, 604800); err != nil {
		return Config{}, fmt.Errorf("ruleset update interval: %w", err)
	}
	config.DNSMode = read("dns_mode")
	if config.DNSMode == "" {
		// Preserve the DNS behavior of older installations that did not have
		// this option. Fresh installs receive an explicit managed-DNS default
		// from files/resproxy.config instead.
		config.DNSMode = "system"
	}
	if config.DNSMode != "proxy-fallback-wan" && config.DNSMode != "proxy-only" && config.DNSMode != "system" {
		return Config{}, errors.New("invalid LAN DNS mode")
	}
	// Legacy global fallback must not override the per-rule outage policy.
	if config.DNSMode == "proxy-fallback-wan" {
		config.DNSMode = "proxy-only"
	}
	handoff := read("dns_handoff_confirmed")
	if handoff != "" && handoff != "0" && handoff != "1" {
		return Config{}, errors.New("invalid DNS handoff confirmation")
	}
	config.DNSHandoffConfirmed = handoff == "1"
	config.DNSProxyURL, err = resolveDNSProvider(read("dns_proxy_protocol"), read("dns_proxy_provider"), read("dns_proxy_url"), false)
	if err != nil {
		return Config{}, fmt.Errorf("dns_proxy_provider: %w", err)
	}
	config.DNSDirectURL, err = resolveDNSProvider(read("dns_direct_protocol"), read("dns_direct_provider"), read("dns_direct_url"), true)
	if err != nil {
		return Config{}, fmt.Errorf("dns_direct_provider: %w", err)
	}
	if config.DNSPort, err = integer(read("dns_port"), 1053, 1024, 65535); err != nil {
		return Config{}, fmt.Errorf("dns_port: %w", err)
	}
	seenSources := map[string]bool{}
	sourceNamePattern := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 _-]{0,31}$`)
	for index := 0; index < 16; index++ {
		section := fmt.Sprintf("resproxy.@subscription[%d].", index)
		id, sectionErr := get(ctx, section+"id")
		if sectionErr != nil {
			break
		}
		readSource := func(key string) string {
			value, _ := get(ctx, section+key)
			return strings.TrimSpace(value)
		}
		source := Subscription{ID: strings.TrimSpace(id), URL: readSource("url"), UpdateMode: readSource("update_mode"), FetchVia: readSource("fetch_via")}
		key := strings.ToLower(source.ID)
		if !sourceNamePattern.MatchString(source.ID) || seenSources[key] {
			return Config{}, fmt.Errorf("subscription %d has invalid or duplicate ID", index+1)
		}
		seenSources[key] = true
		if source.UpdateMode == "" {
			source.UpdateMode = "automatic"
		}
		if source.FetchVia == "" {
			source.FetchVia = "proxy-first"
		}
		if source.UpdateMode != "automatic" && source.UpdateMode != "manual" && source.UpdateMode != "daily" {
			return Config{}, fmt.Errorf("subscription %q has invalid update mode", source.ID)
		}
		if source.UpdateMode == "daily" {
			source.UpdateTime = readSource("update_time")
			if !regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`).MatchString(source.UpdateTime) {
				return Config{}, fmt.Errorf("subscription %q update time must be HH:MM", source.ID)
			}
		}
		if source.FetchVia != "proxy-first" && source.FetchVia != "direct-first" && source.FetchVia != "proxy-only" && source.FetchVia != "direct-only" {
			return Config{}, fmt.Errorf("subscription %q has invalid fetch path", source.ID)
		}
		if source.UpdateSeconds, err = integer(readSource("update_interval_seconds"), 1800, 300, 86400); err != nil {
			return Config{}, fmt.Errorf("subscription %q update interval: %w", source.ID, err)
		}
		if source.MaxNodes, err = integer(readSource("max_nodes"), 1000, 1, 5000); err != nil {
			return Config{}, fmt.Errorf("subscription %q node limit: %w", source.ID, err)
		}
		if err := validateSubscriptionURL(source.URL); err != nil {
			return Config{}, fmt.Errorf("subscription %q: %w", source.ID, err)
		}
		config.Subscriptions = append(config.Subscriptions, source)
	}
	if len(config.Subscriptions) == 0 && config.SubscriptionURL != "" {
		config.Subscriptions = []Subscription{{ID: config.ProviderID, URL: config.SubscriptionURL, UpdateMode: config.UpdateMode, UpdateSeconds: config.UpdateSeconds, MaxNodes: config.MaxNodes, FetchVia: "proxy-first"}}
	}
	for _, field := range []struct {
		key string
		to  *int
	}{
		{"api_port", &config.Base.APIPort}, {"transparent_port", &config.Base.TransparentPort},
		{"http_port", &config.Base.HTTPPort}, {"socks_port", &config.Base.SocksPort}, {"probe_base_port", &config.Base.ProbeBasePort},
		{"dns_policy_base_port", &config.Base.DNSPolicyBasePort},
	} {
		if *field.to, err = integer(read(field.key), *field.to, 1024, 65535); err != nil {
			return Config{}, fmt.Errorf("%s: %w", field.key, err)
		}
	}
	config.Base.ProbeBudget, err = integer(read("probe_batch_size"), 10, 0, 64)
	if err != nil {
		return Config{}, fmt.Errorf("probe budget: %w", err)
	}
	if config.Base.ProbeBudget == 0 {
		config.Base.ProbeBudget = 10
	}
	if config.Base.DNSPolicyBasePort == 19100 && config.Base.ProbeBasePort == 19082 && config.Base.ProbeBudget > 16 {
		config.Base.DNSPolicyBasePort = 19200
	}
	servicePorts := []int{config.Base.APIPort, config.Base.TransparentPort, config.Base.HTTPPort, config.Base.SocksPort, 53}
	for lane := 0; lane <= config.Base.ProbeLaneCount(); lane++ {
		servicePorts = append(servicePorts, config.Base.ProbeBasePort+lane)
	}
	for index := 0; index < 8; index++ {
		servicePorts = append(servicePorts, config.Base.DNSPolicyBasePort+index)
	}
	for _, port := range servicePorts {
		if config.DNSPort == port {
			return Config{}, errors.New("LAN DNS port overlaps another service port")
		}
	}
	sharedHistoryLimit, err := integer(read("history_limit_kib"), 2048, 256, 16384)
	if err != nil {
		return Config{}, fmt.Errorf("history RAM budget: %w", err)
	}
	sharedImprovement := read("switch_improvement_ms")
	if _, err := integer(sharedImprovement, 50, 0, 30000); err != nil {
		return Config{}, fmt.Errorf("shared latency improvement: %w", err)
	}
	ruleByID := map[string]xray.RouteRule{}
	for index := 0; index < 128; index++ {
		section := fmt.Sprintf("resproxy.@rule[%d].", index)
		id, sectionErr := get(ctx, section+"id")
		if sectionErr != nil {
			break
		}
		readRule := func(key string) string {
			value, _ := get(ctx, section+key)
			return strings.TrimSpace(value)
		}
		rule := xray.RouteRule{ID: strings.TrimSpace(id), Action: readRule("action"), OutageAction: readRule("outage_action"), ProxyOutageAction: readRule("proxy_outage_action"),
			Domains: strings.Fields(readRule("domains")), CIDRs: strings.Fields(readRule("cidrs")),
			Name: readRule("name"), Network: readRule("network"),
			Protocols: strings.Fields(readRule("protocols")), Inbounds: strings.Fields(readRule("inbounds")),
			Ports:   readRule("ports"),
			Country: readRule("country"), ServicePacks: strings.Fields(readRule("service_packs"))}
		geoMode := readRule("geo_mode")
		if rule.Country == "" {
			geoMode = "off" // A hidden LuCI default must not activate Geo rules.
		}
		switch geoMode {
		case "", "off":
		case "geosite":
			rule.UseGeosite = true
		case "geoip":
			rule.UseGeoIP = true
		case "both":
			rule.UseGeosite, rule.UseGeoIP = true, true
		default:
			return Config{}, fmt.Errorf("rule group %d has invalid Geo mode", index+1)
		}
		if err := rule.Validate(); err != nil {
			return Config{}, fmt.Errorf("rule group %d: %w", index+1, err)
		}
		if err := rulesets.ValidateSelection(rule.ServicePacks); err != nil {
			return Config{}, fmt.Errorf("rule group %d service_packs: %w", index+1, err)
		}
		if _, exists := ruleByID[rule.ID]; exists {
			return Config{}, fmt.Errorf("duplicate rule group %q", rule.ID)
		}
		ruleByID[rule.ID] = rule
		config.RuleGroups = append(config.RuleGroups, rule)
	}
	config.Base.Policies = nil
	for index := 0; index < 8; index++ {
		section := fmt.Sprintf("resproxy.@policy[%d].", index)
		id, err := get(ctx, section+"id")
		if err != nil || strings.TrimSpace(id) == "" {
			break
		}
		readPolicy := func(key string) string {
			value, _ := get(ctx, section+key)
			return strings.TrimSpace(value)
		}
		policy := Policy{
			ID: strings.TrimSpace(id), Mode: readPolicy("mode"),
			SelectionOrder: strings.Fields(readPolicy("selection_order")),
			NodeFilter: selection.Filter{
				Subscription: readPolicy("selection_source"), Countries: strings.Fields(readPolicy("selection_countries")),
				Protocols: strings.Fields(readPolicy("selection_protocols")), Include: readPolicy("selection_include"), Exclude: readPolicy("selection_exclude"),
			},
			RuleRefs:    strings.Fields(readPolicy("rule_refs")),
			SourceCIDRs: strings.Fields(readPolicy("source_cidrs")),
		}
		seenRules := map[string]bool{}
		if err := policy.NodeFilter.Validate(); err != nil {
			return Config{}, fmt.Errorf("policy %q filter: %w", policy.ID, err)
		}
		orderedRules := make([]xray.RouteRule, 0, len(policy.RuleRefs))
		for _, ref := range policy.RuleRefs {
			rule, exists := ruleByID[ref]
			if !exists || seenRules[ref] {
				return Config{}, fmt.Errorf("policy %q has unknown or duplicate rule group %q", policy.ID, ref)
			}
			seenRules[ref] = true
			orderedRules = append(orderedRules, rule)
		}
		basePolicy := xray.BasePolicy{ID: policy.ID, Rules: orderedRules}
		if _, err := basePolicy.ProxyOutageBehavior(); err != nil {
			return Config{}, err
		}
		policy.WANFallback = basePolicy.HasWANFallback()
		if policy.Mode == "" {
			policy.Mode = "best"
		}
		if policy.Mode != "best" && policy.Mode != "priority" {
			return Config{}, fmt.Errorf("policy %q has invalid mode", policy.ID)
		}
		policy.Health.ActiveLivenessSeconds, err = integer(read("active_liveness_seconds"), 3, 2, 30)
		if err != nil {
			return Config{}, fmt.Errorf("availability interval: %w", err)
		}
		policy.Health.ActiveCheckSeconds, err = integer(read("active_check_seconds"), 60, 1, 3600)
		if err != nil {
			return Config{}, fmt.Errorf("latency interval: %w", err)
		}
		policy.Health.ProbeBatchSize, err = integer(read("probe_batch_size"), 10, 0, 64)
		if err != nil {
			return Config{}, fmt.Errorf("probe budget: %w", err)
		}
		if policy.Health.ProbeBatchSize == 0 {
			policy.Health.ProbeBatchSize = 10
		}
		policy.Health.HistoryLimitKiB = sharedHistoryLimit
		policy.Health.LatencyMeasurement = "https-head-v1"
		improvement := sharedImprovement
		if improvement == "" {
			improvement = readPolicy("switch_improvement_ms")
		}
		policy.Health.SwitchImprovementMS, err = integer(improvement, 50, 0, 30000)
		if err != nil {
			return Config{}, fmt.Errorf("policy %q latency improvement: %w", policy.ID, err)
		}
		config.Policies = append(config.Policies, policy)
		config.Base.Policies = append(config.Base.Policies, xray.BasePolicy{
			ID: policy.ID, SourceCIDRs: policy.SourceCIDRs,
			Rules: orderedRules,
		})
	}
	if len(config.Policies) == 0 {
		fallback := Policy{ID: "main", Mode: "best", Health: health.Policy{HistoryLimitKiB: sharedHistoryLimit, ActiveLivenessSeconds: 3, ActiveCheckSeconds: 60, ProbeBatchSize: 10, SwitchImprovementMS: 50, LatencyMeasurement: "https-head-v1"}}
		for _, field := range []struct {
			key      string
			to       *int
			min, max int
		}{
			{"active_liveness_seconds", &fallback.Health.ActiveLivenessSeconds, 2, 30},
			{"active_check_seconds", &fallback.Health.ActiveCheckSeconds, 1, 3600},
			{"probe_batch_size", &fallback.Health.ProbeBatchSize, 0, 64},
			{"switch_improvement_ms", &fallback.Health.SwitchImprovementMS, 0, 30000},
		} {
			if *field.to, err = integer(read(field.key), *field.to, field.min, field.max); err != nil {
				return Config{}, fmt.Errorf("shared %s: %w", field.key, err)
			}
		}
		if fallback.Health.ProbeBatchSize == 0 {
			fallback.Health.ProbeBatchSize = 10
		}
		config.Policies = []Policy{fallback}
		config.Base.Policies = []xray.BasePolicy{{ID: "main"}}
	}
	config.Base.DefaultPolicy = config.Policies[0].ID
	// Explicit device lists override the unscoped list regardless of UCI order.
	for _, policy := range config.Policies {
		if len(policy.SourceCIDRs) == 0 {
			config.Base.DefaultPolicy = policy.ID
			break
		}
	}
	if err := config.Base.Validate(); err != nil {
		return Config{}, err
	}
	if config.SubscriptionURL != "" {
		if err := validateSubscriptionURL(config.SubscriptionURL); err != nil {
			return Config{}, err
		}
	}
	return config, nil
}

func validateSubscriptionURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("subscription URL must be HTTPS without URL user-info")
	}
	return nil
}

var dnsPresets = map[string]map[string]string{
	"doh": {
		"cloudflare": "https://cloudflare-dns.com/dns-query",
		"google":     "https://dns.google/dns-query",
		"yandex":     "https://common.dot.dns.yandex.net/dns-query",
		"quad9":      "https://dns.quad9.net/dns-query",
		"adguard":    "https://dns.adguard-dns.com/dns-query",
	},
	"dot": {
		"cloudflare": "tls://one.one.one.one:853",
		"google":     "tls://dns.google:853",
		"yandex":     "tls://common.dot.dns.yandex.net:853",
		"quad9":      "tls://dns.quad9.net:853",
		"adguard":    "tls://dns.adguard-dns.com:853",
	},
	"tcp": {
		"cloudflare": "tcp://1.1.1.1:53",
		"google":     "tcp://8.8.8.8:53",
		"yandex":     "tcp://77.88.8.8:53",
		"quad9":      "tcp://9.9.9.9:53",
		"adguard":    "tcp://94.140.14.14:53",
	},
	"udp": {
		"cloudflare": "udp://1.1.1.1:53",
		"google":     "udp://8.8.8.8:53",
		"yandex":     "udp://77.88.8.8:53",
		"quad9":      "udp://9.9.9.9:53",
		"adguard":    "udp://94.140.14.14:53",
	},
}

// An absent protocol/provider is a legacy DoH configuration. Preserve its
// custom URL instead of changing an existing user's resolver on upgrade.
func resolveDNSProvider(protocol, provider, customURL string, direct bool) (string, error) {
	if protocol == "" {
		protocol = "doh"
	}
	if protocol == "udp" && !direct {
		return "", errors.New("plain UDP is not available through the Xray HTTP proxy")
	}
	presets, ok := dnsPresets[protocol]
	if !ok {
		return "", fmt.Errorf("unknown DNS protocol %q", protocol)
	}
	endpoint := customURL
	if provider == "" || provider == "custom" {
		if endpoint == "" && protocol == "doh" {
			endpoint = "https://1.1.1.1/dns-query"
		}
	} else {
		endpoint = presets[provider]
		if endpoint == "" {
			return "", fmt.Errorf("unknown DNS provider %q", provider)
		}
	}
	parsed, err := dnsproxy.ParseEndpoint(endpoint, direct)
	if err != nil {
		return "", err
	}
	if parsed.Protocol != protocol {
		return "", fmt.Errorf("DNS address protocol must be %s", protocol)
	}
	return endpoint, nil
}

func integer(raw string, fallback, minimum, maximum int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("expected integer from %d to %d", minimum, maximum)
	}
	return value, nil
}

func decimal(raw string, fallback, minimum, maximum float64) (float64, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < minimum || value > maximum || value != value {
		return 0, fmt.Errorf("expected number from %g to %g", minimum, maximum)
	}
	return value, nil
}

func boolean(raw string, fallback bool) (bool, error) {
	switch raw {
	case "":
		return fallback, nil
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errors.New("expected 0 or 1")
	}
}
