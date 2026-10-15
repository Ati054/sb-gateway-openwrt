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
		n, err := beginCrashAttempt(paths, testCrashStamp(at))
		if err != nil || n != index+1 {
			t.Fatalf("attempt=%d %v", n, err)
		}
	}
	if err := markCrashReady(paths, testCrashStamp(2100)); err != nil {
		t.Fatal(err)
	}
	if n, err := beginCrashAttempt(paths, testCrashStamp(2200)); err != nil || n != 4 {
		t.Fatalf("brief readiness resets crash loop: %d %v", n, err)
	}
	if err := markCrashReady(paths, testCrashStamp(2300)); err != nil {
		t.Fatal(err)
	}
	if n, err := beginCrashAttempt(paths, testCrashStamp(2600)); err != nil || n != 1 {
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

func testCrashStamp(seconds int64) crashStamp {
	return crashStamp{Seconds: seconds, BootID: "a1234567-1234-1234-1234-123456789012"}
}
func TestCrashBudgetDoesNotTrustLegacyWallClockReadiness(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	old := crashAttempt{Attempts: 3, StartedAt: 100, ReadyAt: 110, PID: os.Getpid()}
	body, _ := json.Marshal(old)
	os.WriteFile(crashFile(paths), body, 0600)
	if n, err := beginCrashAttempt(paths, testCrashStamp(10000000)); err != nil || n != 4 {
		t.Fatalf("wall clock marker reset failure budget: %d %v", n, err)
	}
	now := testCrashStamp(50)
	now.BootID = "b1234567-1234-1234-1234-123456789012"
	if n, err := beginCrashAttempt(paths, now); err != nil || n != 1 {
		t.Fatalf("new boot did not reset: %d %v", n, err)
	}
}
func TestCrashStampUsesKernelUptime(t *testing.T) {
	stamp, err := parseCrashStamp("123.87 44.20\n", testCrashStamp(0).BootID+"\n")
	if err != nil || stamp.Seconds != 123 {
		t.Fatal(stamp, err)
	}
	for _, value := range []string{"NaN 0", "+Inf 0", "-1 0", "1", "bad 0"} {
		if _, err := parseCrashStamp(value, testCrashStamp(0).BootID); err == nil {
			t.Fatal("invalid uptime", value)
		}
	}
}
