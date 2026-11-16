package settings

import (
	"context"
	"strings"
	"testing"
)

func TestEnabledLANRoutingRequiresAnExplicitRouteList(t *testing.T) {
	values := map[string]string{"resproxy.main.enabled": "1", "resproxy.main.route_enabled": "1", "network.lan.device": "br-lan"}
	if _, err := ReadUCI(context.Background(), fake(values)); err == nil || !strings.Contains(err.Error(), "configure a route list") {
		t.Fatal("first activation silently synthesized an all-device route", err)
	}
	values["resproxy.@policy[0].id"] = "main"
	values["resproxy.@policy[0].selection_order"] = "all:*"
	if _, err := ReadUCI(context.Background(), fake(values)); err != nil {
		t.Fatal("configured route rejected", err)
	}
	delete(values, "resproxy.@policy[0].id")
	values["resproxy.main.enabled"] = "0"
	if _, err := ReadUCI(context.Background(), fake(values)); err != nil {
		t.Fatal("disabled first setup could not be edited", err)
	}
}
