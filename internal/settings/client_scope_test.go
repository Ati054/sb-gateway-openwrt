package settings

import (
	"context"
	"strings"
	"testing"
)

func TestScopedOnlyRouteListsRestrictCaptureToTheirClients(t *testing.T) {
	values := map[string]string{
		"resproxy.@policy[0].id": "pc", "resproxy.@policy[0].source_cidrs": "192.0.2.20/32",
		"resproxy.@policy[1].id": "phone", "resproxy.@policy[1].source_cidrs": "2001:db8::20/128",
	}
	config, err := ReadUCI(context.Background(), fake(values))
	if err != nil || strings.Join(config.Capture.ClientCIDRs, " ") != "192.0.2.20/32 2001:db8::20/128" {
		t.Fatalf("selected clients were promoted to a global proxy: %+v %v", config.Capture, err)
	}
	values["resproxy.@policy[2].id"] = "all"
	config, err = ReadUCI(context.Background(), fake(values))
	if err != nil || len(config.Capture.ClientCIDRs) != 0 || config.Base.DefaultPolicy != "all" {
		t.Fatalf("explicit all-device list did not restore common capture: %+v %v", config, err)
	}
}
