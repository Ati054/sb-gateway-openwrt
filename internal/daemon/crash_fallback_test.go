package daemon

import (
	"encoding/json"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
	"os"
	"testing"
)

func TestCrashBudgetCountsSlowFailuresAndResetsOnlyAfterStableReady(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	for index, at := range []int64{1000, 1020, 1500, 2000} {
		n, err := beginCrashAttempt(paths, at)
		if err != nil || n != index+1 {
			t.Fatalf("attempt=%d %v", n, err)
		}
	}
	if err := markCrashReady(paths, 2100); err != nil {
		t.Fatal(err)
	}
	if n, err := beginCrashAttempt(paths, 2200); err != nil || n != 4 {
		t.Fatalf("brief readiness resets crash loop: %d %v", n, err)
	}
	if err := markCrashReady(paths, 2300); err != nil {
		t.Fatal(err)
	}
	if n, err := beginCrashAttempt(paths, 2600); err != nil || n != 1 {
		t.Fatalf("stable recovery not reset: %d %v", n, err)
	}
	var state crashAttempt
	body, _ := os.ReadFile(crashFile(paths))
	if json.Unmarshal(body, &state) != nil || state.ReadyAt != 0 {
		t.Fatal("new launch inherits old readiness")
	}
}

func TestEmergencyAuthorizationUsesDatabaseChoiceNotLegacyPerMatch(t *testing.T) {
	config := settings.Config{Base: xray.BaseConfig{DefaultPolicy: "all", Policies: []xray.BasePolicy{
		{ID: "all", Rules: []xray.RouteRule{{Action: "direct", ProxyOutageAction: "wan"}}},
		{ID: "strict", SourceCIDRs: []string{"192.0.2.9/32"}},
		{ID: "legacy", SourceCIDRs: []string{"192.0.2.10/32"}, Rules: []xray.RouteRule{{Action: "proxy", Domains: []string{"example.com"}, OutageAction: "wan"}}},
	}}}
	policies, err := EmergencyPolicies(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 3 || policies[0].AllowWAN || policies[1].AllowWAN || !policies[2].AllowWAN || len(policies[2].SourceCIDRs) != 0 {
		t.Fatalf("scoped rules broadened: %+v", policies)
	}
	config.Base.DefaultPolicy = "missing"
	if _, err := EmergencyPolicies(config); err == nil {
		t.Fatal("missing default authorizes WAN")
	}
}
