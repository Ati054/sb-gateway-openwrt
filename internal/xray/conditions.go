package xray

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (r RouteRule) hasConditions() bool {
	return r.Network != "" || len(r.Protocols)+len(r.Inbounds) > 0 || r.Ports != ""
}
func validPorts(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 512 {
		return false
	}
	for _, part := range strings.Split(value, ",") {
		edges := strings.Split(part, "-")
		if len(edges) > 2 {
			return false
		}
		previous := 0
		for _, edge := range edges {
			if edge == "" || strings.TrimSpace(edge) != edge {
				return false
			}
			for _, c := range edge {
				if c < '0' || c > '9' {
					return false
				}
			}
			number, err := strconv.Atoi(edge)
			if err != nil || number < 1 || number > 65535 || number < previous {
				return false
			}
			previous = number
		}
	}
	return true
}
func (r RouteRule) validateConditions() error {
	if !utf8.ValidString(r.Name) || utf8.RuneCountInString(r.Name) > 120 || strings.IndexFunc(r.Name, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid rule name %q", r.ID)
	}
	if r.Network != "" && r.Network != "tcp" && r.Network != "udp" && r.Network != "tcp,udp" {
		return fmt.Errorf("invalid network in rule %q", r.ID)
	}
	if !validPorts(r.Ports) {
		return fmt.Errorf("invalid ports in rule %q", r.ID)
	}
	for _, protocol := range r.Protocols {
		if protocol != "http" && protocol != "tls" && protocol != "quic" && protocol != "bittorrent" {
			return fmt.Errorf("invalid protocol in rule %q", r.ID)
		}
	}
	for _, inbound := range r.Inbounds {
		if inbound != "transparent" && inbound != "socks" {
			return fmt.Errorf("invalid inbound in rule %q", r.ID)
		}
	}
	if len(r.Protocols) > 4 || len(r.Inbounds) > 2 {
		return fmt.Errorf("too many conditions in rule %q", r.ID)
	}
	return nil
}

func applyRuleConditions(target map[string]any, r RouteRule, inbounds []string) bool {
	selected := inbounds
	if len(r.Inbounds) > 0 {
		selected = nil
		for _, tag := range inbounds {
			for _, kind := range r.Inbounds {
				if kind == "transparent" && (tag == "resproxy-transparent" || tag == "resproxy-transparent6") || kind == "socks" && tag == "resproxy-socks" {
					selected = append(selected, tag)
					break
				}
			}
		}
	}
	if len(selected) == 0 {
		return false
	}
	target["inboundTag"] = selected
	if r.Network != "" {
		target["network"] = r.Network
	}
	if len(r.Protocols) > 0 {
		target["protocol"] = r.Protocols
	}
	if r.Ports != "" {
		target["port"] = r.Ports
	}

	return true
}
