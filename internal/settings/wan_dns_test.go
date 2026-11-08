package settings

import (
	"github.com/Ati054/sb-gateway-openwrt/internal/dnsproxy"
	"testing"
)

func TestISPDNSIsExplicitWANOnlyAndDoesNotDependOnPreviousProtocol(t *testing.T) {
	for _, protocol := range []string{"", "doh", "dot", "dns", "udp", "tcp"} {
		config, err := ReadUCI(t.Context(), fake(map[string]string{"resproxy.main.dns_direct_provider": "isp", "resproxy.main.dns_direct_protocol": protocol}))
		if err != nil || config.DNSDirectURL != dnsproxy.WANResolverURL {
			t.Fatalf("%s: %+v %v", protocol, config, err)
		}
	}
	if _, err := ReadUCI(t.Context(), fake(map[string]string{"resproxy.main.dns_proxy_provider": "isp"})); err == nil {
		t.Fatal("ISP DNS was accepted for the proxy path")
	}
}

func TestWANPlainDNSUsesAutomaticTransportForNewAndSavedChoices(t *testing.T) {
	for _, protocol := range []string{"dns", "tcp", "udp"} {
		for _, provider := range []string{"cloudflare", "google", "yandex", "quad9", "adguard"} {
			config, err := ReadUCI(t.Context(), fake(map[string]string{"resproxy.main.dns_direct_protocol": protocol, "resproxy.main.dns_direct_provider": provider}))
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := dnsproxy.ParseEndpoint(config.DNSDirectURL, true)
			if err != nil || endpoint.Protocol != "dns" {
				t.Fatalf("%s/%s did not use automatic DNS: %s, %v", protocol, provider, config.DNSDirectURL, err)
			}
		}
		for _, scheme := range []string{"dns", "tcp", "udp"} {
			config, err := ReadUCI(t.Context(), fake(map[string]string{"resproxy.main.dns_direct_protocol": protocol, "resproxy.main.dns_direct_provider": "custom", "resproxy.main.dns_direct_url": scheme + "://192.0.2.53:5353"}))
			if err != nil || config.DNSDirectURL != "dns://192.0.2.53:5353" {
				t.Fatalf("saved custom endpoint changed address or lost automatic transport: %+v, %v", config, err)
			}
		}
	}
	if _, err := ReadUCI(t.Context(), fake(map[string]string{"resproxy.main.dns_proxy_protocol": "dns", "resproxy.main.dns_proxy_provider": "cloudflare"})); err == nil {
		t.Fatal("UDP/TCP automatic DNS was accepted on a TCP-only HTTP proxy path")
	}
}
