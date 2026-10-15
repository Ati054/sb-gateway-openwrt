package settings

import (
	"context"
	"testing"
)

func TestInvalidRuntimeSettingsKeepMinimalProtectionClosed(t *testing.T) {
	c, err := protectionConfig(context.Background(), fake(map[string]string{"resproxy.main.enabled": "1", "resproxy.main.route_enabled": "1", "network.lan.device": "br-lan", "resproxy.main.max_nodes": "bad", "resproxy.main.ipv6_mode": "proxy", "resproxy.main.bypass_cidrs": "192.0.2.0/24"}))
	if err != nil || !c.Enabled || !c.RouteEnabled || c.LANInterface != "br-lan" || len(c.Capture.DirectIPs) != 1 {
		t.Fatal(c, err)
	}
	for _, p := range c.Base.Policies {
		behavior, err := p.ProxyOutageBehavior()
		if err != nil || behavior != "block" {
			t.Fatal("invalid config authorized WAN", behavior, err)
		}
	}
}
func TestMinimalProtectionRequiresKnownLANAndRespectsDisabled(t *testing.T) {
	for _, values := range []map[string]string{{"resproxy.main.enabled": "1", "resproxy.main.route_enabled": "1", "resproxy.main.max_nodes": "bad"}, {"resproxy.main.enabled": "1", "resproxy.main.route_enabled": "1", "network.lan.device": "br-lan;unsafe"}} {
		if _, err := protectionConfig(context.Background(), fake(values)); err == nil {
			t.Fatal("unknown LAN accepted")
		}
	}
	c, err := protectionConfig(context.Background(), fake(map[string]string{"resproxy.main.enabled": "0", "resproxy.main.max_nodes": "bad"}))
	if err != nil || c.Enabled {
		t.Fatal(c, err)
	}
}
