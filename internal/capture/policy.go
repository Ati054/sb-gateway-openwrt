package capture

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

type Policy struct {
	IPv6Mode  string
	DirectIPs []string
	Automatic Inventory
}

func New(mode string, values []string) (Policy, error) {
	if mode == "" {
		mode = "proxy"
	}
	if mode != "proxy" && mode != "direct" && mode != "block" {
		return Policy{}, errors.New("invalid IPv6 mode")
	}
	if len(values) > 256 {
		return Policy{}, errors.New("at most 256 direct IP addresses or networks are allowed")
	}
	result := Policy{IPv6Mode: mode}

	for _, value := range values {
		var prefix netip.Prefix
		if address, err := netip.ParseAddr(value); err == nil && address.Zone() == "" && !address.Is4In6() {
			prefix = netip.PrefixFrom(address, address.BitLen())
		} else {
			var err error
			prefix, err = netip.ParsePrefix(value)
			if err != nil || prefix.Addr().Is4In6() {
				return Policy{}, fmt.Errorf("invalid direct IP address or network: %q", value)
			}
		}
		prefix = prefix.Masked()
		covered := false
		retained := result.DirectIPs[:0]
		for _, current := range result.DirectIPs {
			old := netip.MustParsePrefix(current)
			if old.Addr().BitLen() == prefix.Addr().BitLen() {
				if old.Bits() <= prefix.Bits() && old.Contains(prefix.Addr()) {
					covered = true
				}
				if prefix.Bits() < old.Bits() && prefix.Contains(old.Addr()) {
					continue
				}
			}
			retained = append(retained, current)
		}
		result.DirectIPs = retained
		if !covered {
			result.DirectIPs = append(result.DirectIPs, prefix.String())
		}
	}
	return result, nil
}

// Render uses the kernel routing lookup for LAN destinations. Address/prefix
// changes therefore take effect without saving a discovered network into UCI.
var interfaceName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

func Render(iface string, port, dnsPort int, policy Policy) (string, error) {
	if !interfaceName.MatchString(iface) || port < 1024 || port > 65535 ||
		dnsPort != 0 && (dnsPort < 1024 || dnsPort > 65535 || dnsPort == port) {
		return "", errors.New("invalid capture interface or ports")
	}
	normalized, err := New(policy.IPv6Mode, policy.DirectIPs)
	if err != nil {
		return "", err
	}
	autoIPs := []string{}
	for _, item := range policy.Automatic.Addresses {
		if item.Kind == "endpoint" {
			autoIPs = append(autoIPs, item.IP)
		}
	}
	automatic, err := New("proxy", autoIPs)
	if err != nil {
		return "", err
	}
	normalized.DirectIPs = append(normalized.DirectIPs, automatic.DirectIPs...)
	normalized, err = normalizeCombined(normalized)
	if err != nil {
		return "", err
	}
	tunnelNames := []string{}
	for _, name := range policy.Automatic.Interfaces {
		if !interfaceName.MatchString(name) {
			return "", errors.New("invalid VPN interface")
		}
		tunnelNames = append(tunnelNames, fmt.Sprintf("%q", name))
	}
	if len(tunnelNames) > 32 {
		return "", errors.New("too many VPN interfaces")
	}
	tunnelBypass := func(b *strings.Builder) {
		if len(tunnelNames) > 0 {
			fmt.Fprintf(b, "\t\tiifname %q fib daddr . mark oifname { %s } return\n", iface, strings.Join(tunnelNames, ", "))
		}
	}
	var b strings.Builder
	for _, v6 := range []bool{false, true} {
		family, table, addr := "ip", "resproxy", "ip"
		if v6 {
			family, table, addr = "ip6", "resproxy6", "ip6"
		}
		fmt.Fprintf(&b, "table %s %s {\n", family, table)
		custom := []string{}
		for _, value := range normalized.DirectIPs {
			if strings.Contains(value, ":") == v6 {
				custom = append(custom, value)
			}
		}
		// DNS remains a separate managed service. Explicit destination exceptions
		// also bypass its port-53 redirect. Direct/block IPv6 never redirects DNS.
		b.WriteString("\tchain dns_prerouting {\n\t\ttype nat hook prerouting priority dstnat - 1; policy accept;\n")
		if !v6 || normalized.IPv6Mode == "proxy" {
			// The router's DNS listener remains managed even when a private range
			// is direct. Otherwise the default private ranges disable LAN DNS.
			if dnsPort != 0 {
				for _, proto := range []string{"udp", "tcp"} {
					fmt.Fprintf(&b, "\t\tiifname %q fib daddr type local %s dport 53 redirect to :%d\n", iface, proto, dnsPort)
				}
			}
			tunnelBypass(&b)
			if len(custom) > 0 {
				fmt.Fprintf(&b, "\t\tiifname %q %s daddr { %s } return\n", iface, addr, strings.Join(custom, ", "))
			}
			if dnsPort != 0 {
				for _, proto := range []string{"udp", "tcp"} {
					fmt.Fprintf(&b, "\t\tiifname %q %s dport 53 redirect to :%d\n", iface, proto, dnsPort)
				}
			}
		}
		b.WriteString("\t}\n\tchain prerouting {\n\t\ttype filter hook prerouting priority mangle + 1; policy accept;\n")
		if !v6 || normalized.IPv6Mode != "direct" {
			if v6 {
				// IPv6 neighbour discovery must remain available even with an empty
				// direct list. These link control messages require hop limit 255.
				fmt.Fprintf(&b, "\t\tiifname %q ip6 hoplimit 255 icmpv6 type { nd-router-solicit, nd-router-advert, nd-neighbor-solicit, nd-neighbor-advert } return\n", iface)
			}
			fmt.Fprintf(&b, "\t\tiifname %q fib daddr type local return\n", iface)
			fmt.Fprintf(&b, "\t\tiifname %q fib daddr oifname %q return\n", iface, iface)
			if v6 && normalized.IPv6Mode == "block" {
				// Blocking internet access keeps IPv6 local and control traffic.
				// In proxy mode only the editable direct list exempts these ranges.
				fmt.Fprintf(&b, "\t\tiifname %q ip6 daddr { ::/127, fc00::/7, fe80::/10, ff00::/8 } return\n", iface)
				fmt.Fprintf(&b, "\t\tiifname %q counter drop\n", iface)
			} else {
				tunnelBypass(&b)
				if len(custom) > 0 {
					fmt.Fprintf(&b, "\t\tiifname %q %s daddr { %s } return\n", iface, addr, strings.Join(custom, ", "))
				}
				for _, proto := range []string{"udp", "tcp"} {
					fmt.Fprintf(&b, "\t\tiifname %q %s dport 53 return\n", iface, proto)
				}
				fmt.Fprintf(&b, "\t\tiifname %q meta l4proto { tcp, udp } tproxy to :%d meta mark set 0x5253 accept\n", iface, port)
				if v6 {
					fmt.Fprintf(&b, "\t\tiifname %q counter drop\n", iface)
				} else {
					fmt.Fprintf(&b, "\t\tiifname %q meta l4proto { tcp, udp } drop\n", iface)
				}
			}
		}
		b.WriteString("\t}\n}\n")
	}
	return b.String(), nil
}

// Normalize separately bounded user/runtime lists without rejecting their union.
func normalizeCombined(p Policy) (Policy, error) {
	values := p.DirectIPs
	p.DirectIPs = nil
	for _, value := range values {
		one, err := New(p.IPv6Mode, []string{value})
		if err != nil {
			return Policy{}, err
		}
		prefix := netip.MustParsePrefix(one.DirectIPs[0])
		covered := false
		kept := p.DirectIPs[:0]
		for _, current := range p.DirectIPs {
			old := netip.MustParsePrefix(current)
			if old.Addr().BitLen() == prefix.Addr().BitLen() {
				if old.Bits() <= prefix.Bits() && old.Contains(prefix.Addr()) {
					covered = true
				}
				if prefix.Bits() < old.Bits() && prefix.Contains(old.Addr()) {
					continue
				}
			}
			kept = append(kept, current)
		}
		p.DirectIPs = kept
		if !covered {
			p.DirectIPs = append(p.DirectIPs, prefix.String())
		}
	}
	return p, nil
}
