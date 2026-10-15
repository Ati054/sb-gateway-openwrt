package capture

import (
	"strings"
	"testing"
)

func TestVPNDiscoveryOnlyUsesActiveTunnelRoutesAndLiteralPeers(t *testing.T) {
	kinds := map[string]string{"wg0": "WireGuard", "tun0": "VPN"}
	routes := routeAddresses([]byte(`[{"dst":"default","dev":"wg0"},{"dst":"203.0.113.0/24","dev":"wg0"},{"dst":"198.51.100.1","dev":"tun0"},{"dst":"192.0.2.0/24","dev":"wan"},{"dst":"10.0.0.1","dev":"wg0","type":"local"}]`), kinds)
	if len(routes) != 2 || routes[0].IP != "203.0.113.0/24" || routes[1].IP != "198.51.100.1/32" {
		t.Fatalf("routes: %+v", routes)
	}
	peers := wireGuardAddresses([]byte("wg0 public-key 192.0.2.20:51820\nwg0 public-key [2001:db8::20]:51820\nwg0 public-key (none)\nwg0 public-key hostname:51820\nother key 192.0.2.21:51820\n"), kinds)
	if len(peers) != 2 || peers[0].IP != "192.0.2.20" || peers[1].IP != "2001:db8::20" || strings.Contains(peers[0].Source, "public-key") {
		t.Fatalf("peers: %+v", peers)
	}
	for encoded, want := range map[string]string{"140200C0": "192.0.2.20", "B80D0120000000000000000020000000": "2001:db8::20", "0000000000000000FFFF0000140200C0": "192.0.2.20"} {
		got, ok := socketIP(encoded)
		if !ok || got.String() != want {
			t.Fatalf("socket %s: %s", encoded, got)
		}
	}
}
func TestVPNRoutesKeepKernelPolicyBeforeIPv6Block(t *testing.T) {
	p, _ := New("proxy", []string{"10.0.0.0/8"})
	p.Automatic = Inventory{Interfaces: []string{"wg0", "tun0"}, Addresses: []AutomaticIP{{"192.0.2.20", "WireGuard", "endpoint"}, {"203.0.113.0/24", "VPN", "route"}}}
	body, err := Render("br-lan", 12345, 1053, p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "203.0.113.0/24") {
		t.Fatal("route in another table bypasses policy routing")
	}
	if !strings.Contains(body, `fib daddr . mark oifname { "wg0", "tun0" } return`) || !strings.Contains(body, "192.0.2.20/32") {
		t.Fatal(body)
	}
	if strings.Index(body, "oifname {") > strings.Index(body, "ip daddr { 10.0.0.0/8") || strings.Index(body, "oifname {") > strings.LastIndex(body, "udp dport 53 redirect") {
		t.Fatal("VPN protection after interception")
	}
	p.IPv6Mode = "block"
	body, err = Render("br-lan", 12345, 1053, p)
	if err != nil {
		t.Fatal(err)
	}
	_, v6, _ := strings.Cut(body, "table ip6")
	if !strings.Contains(v6, `iifname "br-lan" fib daddr . mark oifname {`) {
		t.Fatal("IPv6 WAN block breaks explicit VPN routes")
	}
	p.Automatic.Interfaces = []string{"wg0;drop"}
	if _, err = Render("br-lan", 12345, 1053, p); err == nil {
		t.Fatal("unsafe tunnel name accepted")
	}
}

func TestStrictWANPingPreservesVPNRoutePing(t *testing.T) {
	policy, _ := New("block", nil)
	policy.BlockPing = true
	policy.Automatic.Interfaces = []string{"wg0"}
	body, err := Render("br-lan", 12345, 1053, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, proto := range []string{"icmp", "ipv6-icmp"} {
		_, chain, _ := strings.Cut(body, "chain prerouting {")
		if proto == "ipv6-icmp" {
			_, chain, _ = strings.Cut(chain, "chain prerouting {")
		}
		exception := strings.Index(chain, `fib daddr . mark oifname { "wg0" } return`)
		header := "icmp"
		if proto == "ipv6-icmp" {
			header = "icmpv6"
		}
		drop := strings.Index(chain, header+" type { echo-request, echo-reply } counter drop")
		if exception < 0 || drop < 0 || exception > drop {
			t.Fatal("strict WAN ping breaks tunnel ICMP")
		}
	}
}
