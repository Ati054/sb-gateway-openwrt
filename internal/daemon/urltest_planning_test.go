package daemon

import (
	"context"
	"errors"
	"github.com/Ati054/xroute-openwrt/internal/agentport"
	"github.com/Ati054/xroute-openwrt/internal/settings"
	"testing"
)

func TestConfiguredURLTestPlanningModeAndRollback(t *testing.T) {
	for _, v := range []struct {
		raw  string
		want agentport.URLTestPlanningMode
	}{{"", agentport.URLTestPlanningMode(urlTestPlanningDefault)}, {"isolated-paired", agentport.URLTestPlanningRoutePaired}, {"fresh-paired", agentport.URLTestPlanningFreshPaired}} {
		got, e := configuredURLTestPlanningMode(v.raw)
		if e != nil || got != v.want {
			t.Fatalf("%q: %v %v", v.raw, got, e)
		}
	}
	if _, e := configuredURLTestPlanningMode("invalid"); e == nil {
		t.Fatal("unknown measurement admitted")
	}
	a, b := settings.Config{}, settings.Config{URLTestPlanningMode: "isolated-paired"}
	if monitoringOnly(a, b) || !errors.Is(applyMonitoringReload(context.Background(), a, b, nil, ""), ErrRestartRequired) {
		t.Fatal("mode change did not retire old controller")
	}
}

func TestURLTestBuildDefaultAndExplicitRollback(t *testing.T) {
	original := urlTestPlanningDefault
	t.Cleanup(func() { urlTestPlanningDefault = original })
	for _, buildDefault := range []string{"fresh-paired", "route-paired"} {
		urlTestPlanningDefault = buildDefault
		got, err := configuredURLTestPlanningMode("")
		if err != nil || got != agentport.URLTestPlanningMode(buildDefault) {
			t.Fatalf("build default ignored: %v %v", got, err)
		}
		got, err = configuredURLTestPlanningMode("fresh-paired")
		if err != nil || got != agentport.URLTestPlanningFreshPaired {
			t.Fatal("explicit rollback ignored")
		}
	}
	urlTestPlanningDefault = "invalid"
	if _, err := configuredURLTestPlanningMode(""); err == nil {
		t.Fatal("invalid linked build default accepted")
	}
}
