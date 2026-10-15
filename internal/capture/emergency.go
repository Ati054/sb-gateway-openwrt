package capture

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// EmergencyPolicy authorizes WAN for an entire source policy. Per-match legacy
// fallback cannot be reproduced without a live core and therefore stays closed.
type EmergencyPolicy struct {
	SourceCIDRs []string
	AllowWAN    bool
}

// RenderEmergency preserves source-policy precedence and the ordinary capture
// exceptions. It never removes capture globally or authorizes a strict source.
func RenderEmergency(iface string, port int, policy Policy, policies []EmergencyPolicy) (string, error) {
	if len(policies) == 0 || len(policies) > 128 {
		return "", errors.New("invalid emergency source policies")
	}
	ordered := make([]EmergencyPolicy, 0, len(policies))
	var common *EmergencyPolicy
	for _, p := range policies {
		if len(p.SourceCIDRs) == 0 {
			if common != nil {
				return "", errors.New("multiple emergency default policies")
			}
			copy := p
			common = &copy
			continue
		}
		if len(p.SourceCIDRs) > 256 {
			return "", errors.New("too many emergency source prefixes")
		}
		copy := EmergencyPolicy{AllowWAN: p.AllowWAN}
		for _, value := range p.SourceCIDRs {
			prefix, err := netip.ParsePrefix(value)
			if err != nil || prefix.Addr().Is4In6() {
				return "", errors.New("invalid emergency source prefix")
			}
			copy.SourceCIDRs = append(copy.SourceCIDRs, prefix.Masked().String())
		}
		ordered = append(ordered, copy)
	}
	// No common policy means fail closed for every unmatched source.
	if common == nil {
		common = &EmergencyPolicy{}
	}
	ordered = append(ordered, *common)
	return render(iface, port, 0, policy, ordered)
}

func emergencySource(iface, family string, p EmergencyPolicy) (string, bool) {
	match := fmt.Sprintf("\t\tiifname %q", iface)
	if len(p.SourceCIDRs) == 0 {
		return match, true
	}
	values := []string{}
	for _, value := range p.SourceCIDRs {
		if strings.Contains(value, ":") == (family == "ip6") {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return "", false
	}
	return match + fmt.Sprintf(" %s saddr { %s }", family, strings.Join(values, ", ")), true
}

func emergencyDNSGuard(b *strings.Builder, iface, family string, policies []EmergencyPolicy) {
	for _, p := range policies {
		match, ok := emergencySource(iface, family, p)
		if !ok {
			continue
		}
		action := "counter drop"
		if p.AllowWAN {
			action = "return"
		}
		fmt.Fprintf(b, "%s meta l4proto { tcp, udp } th dport 53 %s\n", match, action)
	}
}

func emergencyRules(b *strings.Builder, iface, family string, policies []EmergencyPolicy, dns bool, localOnly bool) {
	for _, p := range policies {
		match, ok := emergencySource(iface, family, p)
		if !ok {
			continue
		}
		if localOnly {
			match += " fib daddr type local"
		}
		action := "counter drop"
		if p.AllowWAN {
			action = "return"
		}
		if dns {
			action = "return"
			if p.AllowWAN {
				action = "redirect to :53"
			}
			fmt.Fprintf(b, "%s meta l4proto { tcp, udp } th dport 53 %s\n", match, action)
		} else {
			fmt.Fprintf(b, "%s meta l4proto { tcp, udp } %s\n", match, action)
		}
	}
}
