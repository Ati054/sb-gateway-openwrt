package capture

import (
	"strings"
	"testing"
)

func TestEmergencyPreservesOverlappingSourcePrecedenceAndDNS(t *testing.T) {
	p, _ := New("proxy", nil)
	// The default may be listed first in UCI. Narrow strict scope must still
	// precede a broader permitted source; neither can inherit default WAN.
	scopes := []EmergencyPolicy{{AllowWAN: true}, {SourceCIDRs: []string{"192.0.2.9/32", "2001:db8::9/128"}}, {SourceCIDRs: []string{"192.0.2.0/24", "2001:db8::/64"}, AllowWAN: true}}
	body, err := RenderEmergency("br-lan", 12345, p, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "tproxy") || strings.Contains(body, "redirect to :1053") {
		t.Fatal("emergency still needs the dead daemon")
	}
	for _, prefix := range []string{"ip saddr { 192.0.2.9/32 }", "ip6 saddr { 2001:db8::9/128 }"} {
		if !strings.Contains(body, prefix+" meta l4proto { tcp, udp } counter drop") || !strings.Contains(body, prefix+" meta l4proto { tcp, udp } th dport 53 counter drop") {
			t.Fatal("strict source not protected")
		}
	}
	_, v4, _ := strings.Cut(body, "chain prerouting {")
	if strings.Index(v4, "jump emergency_dns") >= strings.Index(v4, "fib daddr type local return") {
		t.Fatal("strict DNS can reach restored router resolver")
	}
	if strings.Index(v4, "192.0.2.9/32") >= strings.Index(v4, "192.0.2.0/24") {
		t.Fatal("source precedence changed")
	}
	if !strings.Contains(body, "redirect to :53") {
		t.Fatal("permitted sources have no ordinary DNS")
	}
}

func TestEmergencyMissingDefaultClosesUnmatchedSources(t *testing.T) {
	p, _ := New("proxy", nil)
	body, err := RenderEmergency("br-lan", 12345, p, []EmergencyPolicy{{SourceCIDRs: []string{"192.0.2.1/32"}, AllowWAN: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `iifname "br-lan" meta l4proto { tcp, udp } counter drop`) {
		t.Fatal("unmatched source escapes")
	}
	for _, bad := range [][]EmergencyPolicy{nil, {{}, {AllowWAN: true}}, {{SourceCIDRs: []string{"not-an-IP"}, AllowWAN: true}}} {
		if _, err := RenderEmergency("br-lan", 12345, p, bad); err == nil {
			t.Fatal("invalid emergency authorization accepted")
		}
	}
}

func TestEmergencyDNSPreservesExternalExceptionsWithoutOpeningRouterDNS(t *testing.T) {
	p, _ := New("proxy", []string{"10.0.0.0/8", "2001:db8:99::/64"})
	p.Automatic = Inventory{Complete: true, Interfaces: []string{"wg-office"}}
	body, err := RenderEmergency("br-lan", 12345, p, []EmergencyPolicy{{SourceCIDRs: []string{"192.0.2.9/32", "2001:db8::9/128"}}, {AllowWAN: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range strings.Split(body, "table ")[1:] {
		if !strings.Contains(table, "chain emergency_dns") {
			continue
		}
		_, guard, _ := strings.Cut(table, "chain emergency_dns {")
		guard, _, _ = strings.Cut(guard, "\n\t}")
		vpn := strings.Index(guard, `fib daddr type != local fib daddr . mark oifname { "wg-office" } return`)
		direct := strings.Index(guard, "fib daddr type != local ")
		drop := strings.Index(guard, "th dport 53 counter drop")
		if vpn < 0 || direct < 0 || vpn >= drop || direct >= drop {
			t.Fatal("external DNS exceptions lost", guard)
		}
		_, nat, _ := strings.Cut(table, "chain dns_prerouting {")
		nat, _, _ = strings.Cut(nat, "\n\t}")
		local := strings.Index(nat, "fib daddr type local")
		bypass := strings.Index(nat, "oifname")
		redirect := strings.LastIndex(nat, "redirect to :53")
		if local < 0 || bypass <= local || redirect <= bypass {
			t.Fatal("router and external DNS precedence lost", nat)
		}
	}
}
