package settings

import (
	"github.com/Ati054/sb-gateway-openwrt/internal/dnsproxy"
	"testing"
)

func TestISPDNSIsExplicitWANOnlyAndDoesNotDependOnPreviousProtocol(t *testing.T) {
	for _, protocol := range []string{"", "doh", "dot", "udp", "tcp"} {
		config, err := ReadUCI(t.Context(), fake(map[string]string{"resproxy.main.dns_direct_provider": "isp", "resproxy.main.dns_direct_protocol": protocol}))
		if err != nil || config.DNSDirectURL != dnsproxy.WANResolverURL {
			t.Fatalf("%s: %+v %v", protocol, config, err)
		}
	}
	if _, err := ReadUCI(t.Context(), fake(map[string]string{"resproxy.main.dns_proxy_provider": "isp"})); err == nil {
		t.Fatal("ISP DNS was accepted for the proxy path")
	}
}
