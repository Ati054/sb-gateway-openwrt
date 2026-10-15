package capture

import (
	"strings"
	"testing"
)

func TestValidateAndNormalize(t *testing.T) {
	policy, err := New("proxy", []string{"203.0.113.4", "203.0.113.4/32", "2001:db8:1::123/64"})
	if err != nil || strings.Join(policy.DirectIPs, " ") != "203.0.113.4/32 2001:db8:1::/64" {
		t.Fatalf("%+v %v", policy, err)
	}
	for _, bad := range []string{"999.1.1.1", "1.2.3.4/33", "fe80::1%eth0", "::ffff:192.0.2.1", "1.2.3.4;drop", "geoip:ru"} {
		if _, err := New("proxy", []string{bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := New("disabled", nil); err == nil {
		t.Fatal("accepted invalid mode")
	}
	if _, err := New("proxy", make([]string, 257)); err == nil {
		t.Fatal("unbounded direct list")
	}
}

func TestModesAndPrecedence(t *testing.T) {
	for _, mode := range []string{"proxy", "direct", "block"} {
		policy, _ := New(mode, []string{"100.64.0.0/10", "203.0.113.4", "2001:db8::4"})
		body, err := Render("br-lan", 12345, 1053, policy)
		if err != nil {
			t.Fatal(err)
		}
		v4, v6, _ := strings.Cut(body, "table ip6")
		if strings.Index(v4, "203.0.113.4/32") >= strings.LastIndex(v4, "udp dport 53 redirect") {
			t.Fatal("DNS precedes direct destination")
		}
		if !strings.Contains(v4, "100.64.0.0/10") || !strings.Contains(v4, "fib daddr oifname") || !strings.Contains(v4, "tproxy") {
			t.Fatal("missing IPv4 exclusions/capture")
		}
		switch mode {
		case "proxy":
			if !strings.Contains(v6, "tproxy") || !strings.Contains(v6, "2001:db8::4/128") || !strings.Contains(v6, "redirect") {
				t.Fatal("IPv6 proxy missing")
			}
		case "direct":
			if strings.Contains(v6, "tproxy") || strings.Contains(v6, "redirect") {
				t.Fatal("direct mode intercepts IPv6")
			}
		case "block":
			if strings.Contains(v6, "tproxy") || strings.Contains(v6, "redirect") || !strings.Contains(v6, "2001:db8::4") ||
				!strings.Contains(v6, "fc00::/7") || !strings.Contains(v6, "fib daddr oifname") || !strings.Contains(v6, "counter drop") {
				t.Fatal("IPv6 block does not preserve direct exceptions")
			}
		}
	}
	if _, err := Render("br-lan;drop", 12345, 0, Policy{}); err == nil {
		t.Fatal("unsafe interface accepted")
	}
}

func TestOverlappingExceptions(t *testing.T) {
	policy, err := New("proxy", []string{"203.0.113.4", "203.0.113.0/25", "203.0.113.0/24", "203.0.113.5/32", "2001:db8::/64", "2001:db8::1"})
	if err != nil || strings.Join(policy.DirectIPs, " ") != "203.0.113.0/24 2001:db8::/64" {
		t.Fatalf("overlapping nft intervals: %+v %v", policy, err)
	}
}

// Removing an entry or clearing the saved list must not restore hidden ranges.
func TestEditableDirectListAndRouterDNS(t *testing.T) {
	for _, values := range [][]string{nil, {"100.64.0.0/10", "fc00::/7"}} {
		policy, err := New("proxy", values)
		if err != nil {
			t.Fatal(err)
		}
		body, err := Render("br-lan", 12345, 1053, policy)
		if err != nil {
			t.Fatal(err)
		}
		for _, prefix := range []string{"100.64.0.0/10", "fc00::/7"} {
			if strings.Contains(body, prefix) != (len(values) > 0) {
				t.Fatalf("editable range %s does not follow the saved list", prefix)
			}
		}
		if !strings.Contains(body, "ip6 hoplimit 255 icmpv6 type") {
			t.Fatal("empty direct list breaks IPv6 neighbour discovery")
		}
		if strings.Contains(body, "192.168.0.0/16") {
			t.Fatal("unsaved defaults still active in proxy mode")
		}
		if !strings.Contains(body, "fib daddr type local return") || !strings.Contains(body, "fib daddr oifname") {
			t.Fatal("router and LAN protection missing")
		}
		v4, _, _ := strings.Cut(body, "table ip6")
		router := strings.Index(v4, "fib daddr type local udp dport 53 redirect")
		direct := strings.Index(v4, "100.64.0.0/10")
		if router < 0 || len(values) > 0 && router >= direct {
			t.Fatal("direct list overrides managed router DNS")
		}
	}
}

func TestWANProtocolRestrictionsFollowAllDirectExceptions(t *testing.T) {
	for _, mode := range []string{"proxy", "direct", "block"} {
		for _, strict := range []bool{false, true} {
			p, _ := New(mode, []string{"203.0.113.0/24", "2001:db8::/64"})
			p.BlockPing = strict
			p.Automatic.Interfaces = []string{"wg0"}
			body, err := Render("br-lan", 12345, 1053, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, family := range []string{"ip", "ip6"} {
				start := strings.Index(body, "table "+family+" ")
				if start < 0 {
					t.Fatal("missing family")
				}
			}
			for _, match := range []string{"ip daddr 224.0.0.0/4 return", "fib daddr type broadcast return", "ip6 daddr ff00::/8 return", "meta l4proto icmp ct state related return", "meta l4proto ipv6-icmp ct state related return"} {
				if !strings.Contains(body, match) {
					t.Fatalf("%s mode=%s", match, mode)
				}
			}
			for _, family := range []string{"ip", "ip6"} {
				_, chain, _ := strings.Cut(body, "table "+family+" resproxy")
				_, chain, _ = strings.Cut(chain, "chain prerouting {")
				chain, _, _ = strings.Cut(chain, "chain input {")
				ping := strings.Index(chain, "type { echo-request, echo-reply }")
				for _, exception := range []string{"ct state related return", "fib daddr type local return", `fib daddr oifname "br-lan" return`, `fib daddr . mark oifname { "wg0" } return`, family + " daddr { "} {
					index := strings.Index(chain, exception)
					if index < 0 || index >= ping {
						t.Fatalf("%s/%s strict=%v: exception %s must precede WAN ping", family, mode, strict, exception)
					}
				}
				unsupported := strings.Index(chain, "meta l4proto != { tcp, udp } counter drop")
				if family == "ip6" && mode == "direct" {
					if unsupported >= 0 {
						t.Fatal("IPv6 direct filters GRE/ESP")
					}
				} else if unsupported <= ping {
					t.Fatal("WAN protocol drop must follow ping and direct exceptions")
				}
			}

			action := "return"
			if strict {
				action = "counter drop"
			}
			for _, header := range []string{"icmp", "icmpv6"} {
				if !strings.Contains(body, header+" type { echo-request, echo-reply } "+action) {
					t.Fatal("ping mode not applied")
				}
			}
		}
	}
}

func TestSystemDNSDoesNotBypassExternalResolverTraffic(t *testing.T) {
	policy, _ := New("proxy", nil)
	body, err := Render("br-lan", 12345, 0, policy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "dport 53 return") || strings.Contains(body, "redirect to") {
		t.Fatal("external DNS bypasses traffic policy")
	}
	if !strings.Contains(body, "fib daddr type local return") || !strings.Contains(body, "tproxy to 127.0.0.1:12345") {
		t.Fatal("system DNS or traffic capture missing")
	}
}
