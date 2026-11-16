package daemon

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ati054/xroute-openwrt/internal/settings"
	"github.com/Ati054/xroute-openwrt/internal/xray"
)

func TestRecoveryPlanSurvivesBrokenConfigurationAndKeepsSourcePrecedence(t *testing.T) {
	paths := Paths{DataRoot: t.TempDir(), RunRoot: t.TempDir()}
	config := captureInputFixture()
	config.Subscriptions[0].URL = "https://private.test/subscription-secret"
	config.Base.Policies = []xray.BasePolicy{
		{ID: "strict", SourceCIDRs: []string{"192.0.2.8/32"}, Rules: []xray.RouteRule{{Action: "proxy", ProxyOutageAction: "block"}}},
		{ID: "wan", SourceCIDRs: []string{"192.0.2.0/24"}, Rules: []xray.RouteRule{{Action: "proxy", ProxyOutageAction: "wan"}}},
		{ID: "all", Rules: []xray.RouteRule{{Action: "proxy", ProxyOutageAction: "wan"}}},
	}
	config.Base.DefaultPolicy = "all"
	if err := saveCaptureRecovery(paths, config); err != nil {
		t.Fatal(err)
	}
	path := captureRecoveryPath(paths)
	body, _ := os.ReadFile(path)
	if bytes.Contains(body, []byte("subscription-secret")) {
		t.Fatal("recovery plan contains subscription credentials")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("recovery policy is not private")
	}
	// Corrupting a separately saved future configuration does not affect this
	// plan. An unacknowledged rule edit must never be read by its renderer.
	if err := os.WriteFile(filepath.Join(paths.DataRoot, "invalid-uci"), []byte("dns_port=invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := ReadCaptureRecovery(paths)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := recoveryRules(input)
	if err != nil {
		t.Fatal(err)
	}
	strict := strings.Index(rules, "ip saddr { 192.0.2.8/32 } meta l4proto { tcp, udp } counter drop")
	wan := strings.Index(rules, "ip saddr { 192.0.2.0/24 } meta l4proto { tcp, udp } return")
	if strict < 0 || wan <= strict || strings.Contains(rules, "hook output") || strings.Contains(rules, "tproxy") {
		t.Fatal("strict/WAN precedence or router OUTPUT changed", rules)
	}
	if !strings.Contains(rules, "fib daddr type local return") || !strings.Contains(rules, "ip daddr { 203.0.113.1/32 } return") {
		t.Fatal("management or explicit direct-IP exception lost")
	}
}

func TestRecoveryPlanRejectsCorruptionWithoutAuthorizingGlobalWAN(t *testing.T) {
	for _, body := range []string{`{}`, `{"enabled":true,"route_enabled":true}`, `{"unknown":true}`, strings.Repeat(" ", (1<<20)+1)} {
		paths := Paths{DataRoot: t.TempDir(), RunRoot: t.TempDir()}
		if err := os.WriteFile(captureRecoveryPath(paths), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCaptureRecovery(paths); err == nil {
			t.Fatal("broken plan accepted")
		}
		if err := applyCaptureRecovery(t.Context(), paths); err == nil {
			t.Fatal("broken plan treated as permission for unrestricted WAN")
		}
	}
}

func TestUnconfiguredRouterRecoveryLeavesOrdinaryWANAlone(t *testing.T) {
	paths := Paths{DataRoot: t.TempDir(), RunRoot: t.TempDir()}
	if err := applyCaptureRecovery(t.Context(), paths); err != nil {
		t.Fatal("unconfigured installation tried to invoke firewall", err)
	}
	if err := os.WriteFile(filepath.Join(paths.RunRoot, "firewall.nft"), []byte("active old policy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applyCaptureRecovery(t.Context(), paths); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing active policy silently authorized WAN", err)
	}
}

func TestBootstrapDoesNotCaptureAnUnconfiguredRouter(t *testing.T) {
	paths := Paths{DataRoot: t.TempDir(), RunRoot: t.TempDir()}
	config := settings.Config{Enabled: true, RouteEnabled: true, Base: xray.DefaultBaseConfig()}
	if err := BootstrapCapture(t.Context(), config, paths); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(captureRecoveryPath(paths)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unfinished setup created a blocking guard", err)
	}
}

func TestBootstrapRendererKeepsWANAndStrictScopesWithoutNetifd(t *testing.T) {
	config := captureInputFixture()
	config.Base.Policies = []xray.BasePolicy{
		{ID: "strict", SourceCIDRs: []string{"192.0.2.8/32"}, Rules: []xray.RouteRule{{Action: "proxy", ProxyOutageAction: "block"}}},
		{ID: "main", Rules: []xray.RouteRule{{Action: "proxy", ProxyOutageAction: "wan"}}},
	}
	body, err := EncodeCaptureRestore(config)
	if err != nil {
		t.Fatal(err)
	}
	input, err := DecodeCaptureRestore(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := RenderBootstrapCapture(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rules, "# resproxy-restore guard br-lan") || !strings.Contains(rules, "ip saddr { 192.0.2.8/32 } meta l4proto { tcp, udp } counter drop") || !strings.Contains(rules, `iifname "br-lan" meta l4proto { tcp, udp } return`) || strings.Contains(rules, "hook output") || strings.Contains(rules, "tproxy") {
		t.Fatal("boot guard changed routing authorization or needs the core", rules)
	}
}
