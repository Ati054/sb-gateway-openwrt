package capture

import (
	"strings"
	"testing"
)

func TestSelectedClientScopePrecedesDNSAndProtocolFiltering(t *testing.T) {
	for _, emergency := range []bool{false, true} {
		policy := Policy{IPv6Mode: "proxy", BlockPing: true, ClientCIDRs: []string{"192.0.2.0/24", "192.0.2.20/32", "2001:db8::20/128"}}
		var body string
		var err error
		if emergency {
			body, err = RenderEmergency("br-lan", 12345, policy, []EmergencyPolicy{{SourceCIDRs: policy.ClientCIDRs}})
		} else {
			body, err = Render("br-lan", 12345, 1053, policy)
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, guard := range []string{`iifname "br-lan" ip saddr != { 192.0.2.0/24 } return`, `iifname "br-lan" ip6 saddr != { 2001:db8::20/128 } return`} {
			if strings.Count(body, guard) != 2 {
				t.Fatalf("source gate missing from DNS/traffic chains: %q", guard)
			}
		}
		for _, family := range []string{"ip", "ip6"} {
			part := strings.Split(body, "table "+family+" ")
			partBody := part[len(part)-1]
			for _, chain := range []string{"dns_prerouting", "prerouting"} {
				_, rules, ok := strings.Cut(partBody, "chain "+chain+" {")
				if !ok {
					t.Fatal("missing chain", chain)
				}
				rules, _, _ = strings.Cut(rules, "\n\t}")
				gate := strings.Index(rules, "saddr !=")
				if gate < 0 || strings.Index(rules, "counter drop") >= 0 && gate > strings.Index(rules, "counter drop") {
					t.Fatal("unselected clients encounter protocol filtering before bypass")
				}
			}
		}
	}
	policy := Policy{IPv6Mode: "block", ClientCIDRs: []string{"192.0.2.20/32"}}
	body, err := Render("br-lan", 12345, 1053, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, v6, _ := strings.Cut(body, "table ip6 resproxy6 {")
	if strings.Count(v6, "\n\t\tiifname \"br-lan\" return\n") != 2 {
		t.Fatal("IPv4-only device selection captures unrelated IPv6 clients")
	}
	policy.ClientCIDRs = []string{"192.0.2.20;drop"}
	if _, err := Render("br-lan", 12345, 1053, policy); err == nil {
		t.Fatal("invalid client scope accepted")
	}
}
