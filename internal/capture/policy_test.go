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
			if strings.Contains(v6, "drop") || strings.Contains(v6, "tproxy") || strings.Contains(v6, "redirect") {
				t.Fatal("direct mode intercepts IPv6")
			}
		case "block":
			if strings.Contains(v6, "tproxy") || strings.Contains(v6, "redirect") || strings.Contains(v6, "2001:db8::4") ||
				!strings.Contains(v6, "fc00::/7") || !strings.Contains(v6, "fib daddr oifname") || !strings.Contains(v6, "counter drop") {
				t.Fatal("IPv6 block does not preserve LAN or bypasses block")
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
		if strings.Contains(body, "192.168.0.0/16") || strings.Contains(body, "ff00::/8") {
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
