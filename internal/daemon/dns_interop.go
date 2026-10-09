package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// DNSInterop records what actually happened, rather than presenting an opted-in
// DNS mode as active when another resolver already owns LAN DNS interception.
type DNSInterop struct {
	RequestedMode      string `json:"requested_mode"`
	EffectiveMode      string `json:"effective_mode"`
	Reason             string `json:"reason,omitempty"`
	ConflictingService string `json:"conflicting_service,omitempty"`
}

var dnsPort53 = regexp.MustCompile(`(^|[^0-9])53([^0-9]|$)`)

func dnsInteropPath(paths Paths) string {
	return filepath.Join(paths.RunRoot, "dns-interop.json")
}

func publishDNSInterop(paths Paths, state DNSInterop) error {
	content, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(dnsInteropPath(paths), append(content, '\n'), 0o600)
}

func resolveDNSInterop(ctx context.Context, requested string, nftRules func(context.Context) (string, error), externalDNSService func() string) DNSInterop {
	state := DNSInterop{RequestedMode: requested, EffectiveMode: requested}
	if requested == "system" {
		return state
	}
	// An external resolver may own dnsmasq's upstream even without a port-53
	// redirect. Keep DNS ownership exclusive; never change its configuration.
	if service := externalDNSService(); service != "" {
		state.EffectiveMode = "system"
		state.ConflictingService = service
		if service == "https-dns-proxy" {
			state.Reason = "https-dns-proxy-running"
		} else {
			state.Reason = "other-dns-service"
		}
		return state
	}
	rules, err := nftRules(ctx)
	if err != nil {
		state.EffectiveMode = "system"
		state.Reason = "firewall-inspection-failed"
		return state
	}
	if hasCompetingDNSRedirect(rules) {
		state.EffectiveMode = "system"
		state.Reason = "other-dns-redirect"
	}
	return state
}

func activeExternalDNSService() string {
	return externalDNSServiceAt("/etc/init.d", func(path string) bool {
		return exec.Command(path, "running").Run() == nil
	})
}

func ActiveExternalDNSService() string {
	return activeExternalDNSService()
}

func externalDNSServiceAt(initDir string, running func(string) bool) string {
	for _, service := range []string{
		"https-dns-proxy", "smartdns", "mosdns", "adguardhome",
		"AdGuardHome", "unbound", "dnscrypt-proxy", "dnscrypt-proxy2",
	} {
		path := filepath.Join(initDir, service)
		if _, err := os.Stat(path); err == nil && running(path) {
			return service
		}
	}
	return ""
}

func hasCompetingDNSRedirect(rules string) bool {
	table := ""
	for _, line := range strings.Split(rules, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "table ") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				table = fields[2]
			}
			continue
		}
		if table == "resproxy" || table == "resproxy6" {
			continue
		}
		if strings.Contains(line, "dport") && dnsPort53.MatchString(line) &&
			(strings.Contains(line, " redirect") || strings.Contains(line, " dnat") || strings.Contains(line, " tproxy")) {
			return true
		}
	}
	return false
}

func currentNFTRules(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "nft", "-a", "list", "ruleset").Output()
	if err != nil {
		return "", fmt.Errorf("inspect nftables: %w", err)
	}
	return string(output), nil
}
