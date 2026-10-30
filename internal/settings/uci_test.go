package settings

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func fake(values map[string]string) Getter {
	return func(_ context.Context, key string) (string, error) {
		if value, found := values[key]; found {
			return value, nil
		}
		return "", errors.New("missing UCI option")
	}
}

func TestReadUCIWithPoliciesAndMonitorIntervals(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.enabled": "1", "resproxy.main.subscription_url": "https://example.test/feed",
		"resproxy.@policy[0].id": "home", "resproxy.@policy[0].mode": "priority",
		"resproxy.@policy[0].selection_order":         "country:CA country:US",
		"resproxy.@policy[0].source_cidrs":            "192.168.10.0/24",
		"resproxy.@policy[0].active_liveness_seconds": "3",
		"resproxy.@policy[0].failure_retry_seconds":   "2",
		"resproxy.@policy[0].block_recovery_seconds":  "15",
	}))
	if err != nil || !config.Enabled || len(config.Policies) != 1 || config.Policies[0].Mode != "priority" ||
		len(config.Policies[0].SelectionOrder) != 2 || config.Policies[0].Health.ActiveLivenessSeconds != 3 {
		t.Fatalf("unexpected UCI config: %+v %v", config, err)
	}
}

func TestReadUCISharedMonitorOverridesOldPerListIntervals(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.active_liveness_seconds":       "5",
		"resproxy.main.failure_retry_seconds":         "2",
		"resproxy.main.active_check_seconds":          "70",
		"resproxy.main.backup_check_seconds":          "400",
		"resproxy.main.full_scan_seconds":             "1900",
		"resproxy.main.probe_batch_size":              "0",
		"resproxy.@policy[0].id":                      "first",
		"resproxy.@policy[0].mode":                    "best",
		"resproxy.@policy[0].active_liveness_seconds": "3",
		"resproxy.@policy[0].probe_batch_size":        "3",
		"resproxy.@policy[1].id":                      "second",
		"resproxy.@policy[1].mode":                    "priority",
		"resproxy.@policy[1].active_liveness_seconds": "10",
	}))
	if err != nil || len(config.Policies) != 2 {
		t.Fatalf("failed to read two route lists: %v", err)
	}
	for _, policy := range config.Policies {
		if policy.Health.ActiveLivenessSeconds != 5 || policy.Health.ActiveCheckSeconds != 70 || policy.Health.ProbeBatchSize != 10 {
			t.Fatalf("shared monitor settings did not win for %s: %+v", policy.ID, policy.Health)
		}
	}
}

func TestReadUCILoadsSharedDefaultsAndPerListSwitchRules(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.@policy[0].id":                        "main",
		"resproxy.@policy[0].active_liveness_seconds":   "4",
		"resproxy.@policy[0].failure_retry_seconds":     "2",
		"resproxy.@policy[0].probe_batch_size":          "4",
		"resproxy.@policy[0].max_active_candidates":     "7",
		"resproxy.@policy[0].max_packet_loss_percent":   "22.5",
		"resproxy.@policy[0].speed_improvement_percent": "35",
	}))
	if err != nil || len(config.Policies) != 1 {
		t.Fatalf("legacy route list rejected: %v", err)
	}
	policy := config.Policies[0].Health
	if policy.ActiveLivenessSeconds != 3 || policy.ProbeBatchSize != 10 || policy.SwitchImprovementMS != 50 {
		t.Fatalf("legacy or per-list values lost: %+v", policy)
	}
}

func TestReadUCISharedMonitorWithoutExplicitRouteList(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.active_liveness_seconds": "6",
		"resproxy.main.probe_batch_size":        "0",
	}))
	if err != nil || len(config.Policies) != 1 || config.Policies[0].Health.ActiveLivenessSeconds != 6 || config.Policies[0].Health.ProbeBatchSize != 10 {
		t.Fatalf("default route ignored shared monitor: %+v %v", config.Policies, err)
	}
}

func TestReadUCISharedIntervalsCannotInvalidateSavedRoute(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.active_liveness_seconds": "2",
		"resproxy.main.failure_retry_seconds":   "10",
		"resproxy.main.active_check_seconds":    "600",
		"resproxy.main.backup_check_seconds":    "300",
		"resproxy.main.full_scan_seconds":       "100",
		"resproxy.@policy[0].id":                "main",
	}))
	if err != nil || len(config.Policies) != 1 {
		t.Fatalf("individually valid shared intervals stopped a saved route: %v", err)
	}
}

func TestReadUCIReusableRuleGroups(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.@rule[0].id":          "office",
		"resproxy.@rule[0].action":      "direct",
		"resproxy.@rule[0].domains":     "example.com\nfull:login.example.com",
		"resproxy.@rule[0].cidrs":       "203.0.113.0/24",
		"resproxy.@policy[0].id":        "main",
		"resproxy.@policy[0].rule_refs": "office",
	}))
	if err != nil || len(config.RuleGroups) != 1 || len(config.Base.Policies[0].Rules) != 1 ||
		len(config.Base.Policies[0].Rules[0].Domains) != 2 {
		t.Fatalf("reusable rule group not loaded: %+v %v", config, err)
	}
}

func TestReadUCICountryRuleModes(t *testing.T) {
	for _, mode := range []string{"geosite", "geoip", "both"} {
		config, err := ReadUCI(context.Background(), fake(map[string]string{
			"resproxy.@rule[0].id": "country-ru", "resproxy.@rule[0].action": "direct",
			"resproxy.@rule[0].country": "ru", "resproxy.@rule[0].geo_mode": mode,
			"resproxy.@policy[0].id": "main", "resproxy.@policy[0].rule_refs": "country-ru",
		}))
		if err != nil || len(config.RuleGroups) != 1 {
			t.Fatalf("country mode %s rejected: %v", mode, err)
		}
		rule := config.RuleGroups[0]
		if rule.UseGeosite != (mode != "geoip") || rule.UseGeoIP != (mode != "geosite") {
			t.Fatalf("country mode %s: %+v", mode, rule)
		}
	}
	for _, mode := range []string{"unknown", "off"} {
		if _, err := ReadUCI(context.Background(), fake(map[string]string{
			"resproxy.@rule[0].id": "country-ru", "resproxy.@rule[0].action": "direct",
			"resproxy.@rule[0].country": "ru", "resproxy.@rule[0].geo_mode": mode,
		})); err == nil {
			t.Fatalf("invalid empty country rule accepted: %s", mode)
		}
	}
}

func TestReadUCIRejectsUnsafeURLAndBadTiming(t *testing.T) {
	for _, values := range []map[string]string{
		{"resproxy.main.subscription_url": "http://insecure.test/feed"},
		{"resproxy.@policy[0].id": "main", "resproxy.main.active_liveness_seconds": "1"},
		{"resproxy.main.api_port": "19080"},
		{"resproxy.main.route_enabled": "1", "network.lan.device": "wg0;reboot"},
		{"resproxy.main.update_mode": "sometimes"},
		{"resproxy.main.dns_mode": "unknown"},
		{"resproxy.main.dns_handoff_confirmed": "maybe"},
		{"resproxy.main.dns_proxy_url": "http://1.1.1.1/dns-query"},
		{"resproxy.main.dns_direct_url": "https://user:pass@1.1.1.1/dns-query"},
		{"resproxy.main.dns_proxy_provider": "unknown"},
		{"resproxy.main.dns_proxy_protocol": "udp"},
		{"resproxy.main.dns_direct_protocol": "quic"},
		{"resproxy.main.dns_proxy_protocol": "dot", "resproxy.main.dns_proxy_provider": "custom", "resproxy.main.dns_proxy_url": "https://dns.example/dns-query"},
		{"resproxy.main.dns_direct_protocol": "tcp", "resproxy.main.dns_direct_provider": "custom", "resproxy.main.dns_direct_url": "tcp://dns.example:53"},
		{"resproxy.main.dns_port": "12345"},
		{"resproxy.@policy[0].id": "main", "resproxy.@policy[0].rule_refs": "missing"},
		{"resproxy.@rule[0].id": "bad", "resproxy.@rule[0].action": "direct", "resproxy.@rule[0].domains": "*.example.com"},
	} {
		if _, err := ReadUCI(context.Background(), fake(values)); err == nil {
			t.Fatalf("unsafe UCI config accepted: %+v", values)
		}
	}
}

func TestReadUCIDNSDefaults(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(nil))
	if err != nil || config.DNSMode != "system" || config.DNSPort != 1053 ||
		config.DNSProxyURL != "https://1.1.1.1/dns-query" {
		t.Fatalf("unexpected DNS defaults: %+v %v", config, err)
	}
}

func TestReadUCIDNSProtocols(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.dns_proxy_protocol":  "dot",
		"resproxy.main.dns_proxy_provider":  "quad9",
		"resproxy.main.dns_direct_protocol": "udp",
		"resproxy.main.dns_direct_provider": "adguard",
	}))
	if err != nil || config.DNSProxyURL != "tls://dns.quad9.net:853" ||
		config.DNSDirectURL != "udp://94.140.14.14:53" {
		t.Fatalf("DNS protocols not resolved: %+v %v", config, err)
	}
	if _, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.dns_proxy_protocol": "udp",
		"resproxy.main.dns_proxy_provider": "cloudflare",
	})); err == nil {
		t.Fatal("plain UDP through the HTTP proxy was accepted")
	}
}

func TestReadUCIDNSProviderPresetsAndLegacyURL(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.dns_proxy_provider":  "google",
		"resproxy.main.dns_direct_provider": "yandex",
		"resproxy.main.dns_proxy_url":       "https://old.example/dns-query",
	}))
	if err != nil || config.DNSProxyURL != "https://dns.google/dns-query" ||
		config.DNSDirectURL != "https://common.dot.dns.yandex.net/dns-query" {
		t.Fatalf("DNS presets not resolved: %+v %v", config, err)
	}
	legacy, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.dns_proxy_url": "https://resolver.example/dns-query",
	}))
	if err != nil || legacy.DNSProxyURL != "https://resolver.example/dns-query" {
		t.Fatalf("legacy DNS URL was replaced: %+v %v", legacy, err)
	}
}

func TestReadUCICloudflareWANUsesCertificateValidatedIPEndpoint(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.dns_proxy_protocol":  "doh",
		"resproxy.main.dns_proxy_provider":  "cloudflare",
		"resproxy.main.dns_direct_protocol": "doh",
		"resproxy.main.dns_direct_provider": "cloudflare",
	}))
	if err != nil || config.DNSDirectURL != "https://1.1.1.1/dns-query" ||
		config.DNSProxyURL != "https://cloudflare-dns.com/dns-query" {
		t.Fatalf("Cloudflare endpoints not resolved by path: %+v %v", config, err)
	}
	custom, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.dns_direct_protocol": "doh",
		"resproxy.main.dns_direct_provider": "custom",
		"resproxy.main.dns_direct_url":      "https://cloudflare-dns.com/dns-query",
	}))
	if err != nil || custom.DNSDirectURL != "https://cloudflare-dns.com/dns-query" {
		t.Fatalf("custom WAN endpoint replaced: %+v %v", custom, err)
	}
}

func TestReadUCIIgnoresRemovedBootstrapDNSOptions(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.dns_bootstrap_enabled": "1",
		"resproxy.main.dns_bootstrap_port":    "1154",
	}))
	if err != nil || config.DNSMode != "system" {
		t.Fatalf("removed bootstrap DNS options changed active DNS: %+v %v", config, err)
	}
}

func TestReadUCIManualRefreshMode(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.update_mode": "manual",
	}))
	if err != nil || config.UpdateMode != "manual" {
		t.Fatalf("manual update mode not loaded: %q, %v", config.UpdateMode, err)
	}
}

func TestReadUCIRulesetsAlwaysAutomatic(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.ruleset_update_mode": "manual",
	}))
	if err != nil || config.RulesetUpdateMode != "automatic" {
		t.Fatalf("legacy manual rulesets left enabled: %q, %v", config.RulesetUpdateMode, err)
	}
}

func TestReadUCIMultipleSubscriptionSources(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.@subscription[0].id":          "primary",
		"resproxy.@subscription[0].url":         "https://one.test/feed",
		"resproxy.@subscription[0].fetch_via":   "direct-only",
		"resproxy.@subscription[1].id":          "reserve",
		"resproxy.@subscription[1].url":         "https://two.test/feed",
		"resproxy.@subscription[1].update_mode": "manual",
		"resproxy.@subscription[1].fetch_via":   "proxy-only",
	}))
	if err != nil || len(config.Subscriptions) != 2 || config.Subscriptions[0].FetchVia != "direct-only" || config.Subscriptions[1].UpdateMode != "manual" {
		t.Fatalf("multiple sources not loaded: %+v %v", config.Subscriptions, err)
	}
}

func TestReadUCISourceNameKeepsCapitalsAndSpaces(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.@subscription[0].id":  "Office VPN",
		"resproxy.@subscription[0].url": "https://one.test/feed",
	}))
	if err != nil || len(config.Subscriptions) != 1 || config.Subscriptions[0].ID != "Office VPN" {
		t.Fatalf("source name changed or rejected: %+v %v", config.Subscriptions, err)
	}
}

func TestReadUCIRejectsInvalidSources(t *testing.T) {
	for _, change := range []map[string]string{
		{"resproxy.@subscription[0].url": "http://insecure.test/feed"},
		{"resproxy.@subscription[1].id": "primary"},
		{"resproxy.@subscription[1].id": "PRIMARY"},
		{"resproxy.@subscription[0].fetch_via": "random"},
		{"resproxy.@subscription[0].update_mode": "hourly"},
	} {
		values := map[string]string{
			"resproxy.@subscription[0].id":  "primary",
			"resproxy.@subscription[0].url": "https://one.test/feed",
			"resproxy.@subscription[1].id":  "reserve",
			"resproxy.@subscription[1].url": "https://two.test/feed",
		}
		for key, value := range change {
			values[key] = value
		}
		if _, err := ReadUCI(context.Background(), fake(values)); err == nil {
			t.Fatalf("invalid source accepted: %+v", change)
		}
	}
}

func TestReadUCIAllDevicePolicyRemainsDefaultAfterReordering(t *testing.T) {
	for _, ids := range [][]string{{"main", "office"}, {"office", "main"}} {
		data := map[string]string{}
		for i, id := range ids {
			key := fmt.Sprintf("resproxy.@policy[%d].", i)
			data[key+"id"] = id
			if id == "office" {
				data[key+"source_cidrs"] = "192.168.1.20/32 fd00::20/128"
			}
		}
		config, err := ReadUCI(context.Background(), fake(data))
		if err != nil {
			t.Fatal(err)
		}
		if config.Base.DefaultPolicy != "main" {
			t.Fatalf("order %v default=%s", ids, config.Base.DefaultPolicy)
		}
	}
}

func TestReadUCINodeFilter(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.@policy[0].id": "main", "resproxy.@policy[0].selection_order": "all:*",
		"resproxy.@policy[0].selection_source": "Source A", "resproxy.@policy[0].selection_countries": "CA US",
		"resproxy.@policy[0].selection_protocols": "vless", "resproxy.@policy[0].selection_include": "🇨🇦, premium",
		"resproxy.@policy[0].selection_exclude": "test, trial",
	}))
	if err != nil {
		t.Fatal(err)
	}
	filter := config.Policies[0].NodeFilter
	if filter.Subscription != "Source A" || len(filter.Countries) != 2 || filter.Include != "🇨🇦, premium" || filter.Exclude != "test, trial" {
		t.Fatalf("filter lost: %+v", filter)
	}
}

func TestReadUCIIgnoresRetiredSpeedControls(t *testing.T) {
	for _, value := range []string{"0", "50", "100", "bad"} {
		config, err := ReadUCI(context.Background(), fake(map[string]string{"resproxy.@policy[0].id": "main", "resproxy.@policy[0].speed_degradation_percent": value, "resproxy.main.probe_batch_size": "64"}))
		if err != nil || config.Policies[0].Health.ProbeBatchSize != 64 {
			t.Fatalf("retired control blocks migration: %v", err)
		}
	}
}

func TestSharedURLTestImprovementPreservesLegacyUntilSaved(t *testing.T) {
	values := map[string]string{
		"resproxy.@policy[0].id": "a", "resproxy.@policy[0].switch_improvement_ms": "75",
		"resproxy.@policy[1].id": "b", "resproxy.@policy[1].switch_improvement_ms": "120",
	}
	cfg, err := ReadUCI(context.Background(), fake(values))
	if err != nil || cfg.Policies[0].Health.SwitchImprovementMS != 75 || cfg.Policies[1].Health.SwitchImprovementMS != 120 {
		t.Fatal(cfg, err)
	}
	values["resproxy.main.switch_improvement_ms"] = "90"
	values["resproxy.@policy[0].switch_improvement_ms"] = "retired-invalid"
	cfg, err = ReadUCI(context.Background(), fake(values))
	if err != nil || cfg.Policies[0].Health.SwitchImprovementMS != 90 || cfg.Policies[1].Health.SwitchImprovementMS != 90 {
		t.Fatal(cfg, err)
	}
	delete(values, "resproxy.@policy[0].id")
	delete(values, "resproxy.@policy[1].id")
	cfg, err = ReadUCI(context.Background(), fake(values))
	if err != nil || cfg.Policies[0].Health.SwitchImprovementMS != 90 {
		t.Fatal(cfg, err)
	}
	values["resproxy.main.switch_improvement_ms"] = "30001"
	if _, err = ReadUCI(context.Background(), fake(values)); err == nil {
		t.Fatal("invalid shared improvement accepted")
	}
}
