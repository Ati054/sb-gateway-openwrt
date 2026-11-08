package dnsproxy

import "testing"

// netifd retains DHCP-advertised DNS in inactive.dns-server when peerdns=0.
// Explicitly selecting the ISP resolver must not require enabling system DNS
// or changing the network interface configuration first.
func TestExplicitWANDNSFindsPeerResolversWhenSystemPeerDNSIsDisabled(t *testing.T) {
	body := []byte(`{"interface":[{"interface":"wan","up":true,"proto":"dhcp","l3_device":"eth1","dns-server":[],"route":[{"target":"0.0.0.0","mask":0,"nexthop":"192.0.2.1"}],"inactive":{"dns-server":["192.0.2.53","192.0.2.54"]}}]}`)
	servers, err := parseWANDNS(body, nil)
	if err != nil || len(servers) != 2 {
		t.Fatalf("DHCP resolvers disappeared with system peerdns disabled: %v, %v", servers, err)
	}
	if servers[0].Address != "192.0.2.53:53" || servers[1].Address != "192.0.2.54:53" || servers[0].Interface != "eth1" {
		t.Fatalf("wrong peer resolvers: %+v", servers)
	}
}

func TestWANInactivePeerDNSPreservesActiveServersAndUplinkRequirements(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"active DNS is preferred", `{"interface":[{"up":true,"proto":"dhcp","l3_device":"eth1","dns-server":["192.0.2.54"],"route":[{"target":"0.0.0.0","mask":0}],"inactive":{"dns-server":["192.0.2.53"]}}]}`, "192.0.2.54:53"},
		{"loopback active DNS is not a usable upstream", `{"interface":[{"up":true,"proto":"dhcp","l3_device":"eth1","dns-server":["127.0.0.1","::1"],"route":[{"target":"0.0.0.0","mask":0}],"inactive":{"dns-server":["192.0.2.53"]}}]}`, "192.0.2.53:53"},
		{"IPv6 peer retains device scope", `{"interface":[{"up":true,"proto":"dhcpv6","l3_device":"eth1","route":[{"target":"::","mask":0}],"inactive":{"dns-server":["fe80::1"]}}]}`, "[fe80::1%eth1]:53"},
		{"down WAN is excluded", `{"interface":[{"up":false,"proto":"dhcp","l3_device":"eth1","route":[{"target":"0.0.0.0","mask":0}],"inactive":{"dns-server":["192.0.2.53"]}}]}`, ""},
		{"inactive default route cannot supply WAN", `{"interface":[{"up":true,"proto":"dhcp","l3_device":"eth1","inactive":{"route":[{"target":"0.0.0.0","mask":0}],"dns-server":["192.0.2.53"]}}]}`, ""},
		{"VPN peers are excluded", `{"interface":[{"up":true,"proto":"wireguard","l3_device":"wg0","route":[{"target":"0.0.0.0","mask":0}],"inactive":{"dns-server":["192.0.2.53"]}}]}`, ""},
		{"local peer is excluded", `{"interface":[{"up":true,"proto":"dhcp","l3_device":"eth1","route":[{"target":"0.0.0.0","mask":0}],"inactive":{"dns-server":["127.0.0.1","::1"]}}]}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			servers, err := parseWANDNS([]byte(tc.body), nil)
			if tc.want == "" {
				if err == nil || len(servers) != 0 {
					t.Fatalf("unusable WAN accepted: %+v, %v", servers, err)
				}
				return
			}
			if err != nil || len(servers) != 1 || servers[0].Address != tc.want || servers[0].Interface != "eth1" {
				t.Fatalf("wrong WAN DNS: %+v, %v", servers, err)
			}
		})
	}
}
