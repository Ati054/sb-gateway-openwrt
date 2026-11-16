package xray

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"unicode"
)

// NormalizeRuleIP keeps the routing compiler's canonical-prefix invariant.
// A single host has the same meaning as its /32 or /128 prefix.
func NormalizeRuleIP(value string) (string, error) {
	value = strings.TrimSpace(value)
	if address, err := netip.ParseAddr(value); err == nil {
		if address.Zone() != "" || address.Is4In6() {
			return "", errors.New("invalid IP address")
		}
		return netip.PrefixFrom(address, address.BitLen()).String(), nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
		return "", errors.New("invalid IP address or network")
	}
	return prefix.Masked().String(), nil
}

type RuleInputResult struct {
	Valid bool   `json:"valid"`
	Field string `json:"field,omitempty"`
	Index int    `json:"index"`
	Error string `json:"error,omitempty"`
}

// ValidateRuleInput is a bounded, read-only LuCI pre-save operation. In
// particular, regular expressions use Go/Xray syntax, not JavaScript syntax.
func ValidateRuleInput(body string) (RuleInputResult, error) {
	var input struct {
		Domains []string `json:"domains"`
		CIDRs   []string `json:"cidrs"`
	}
	if len(body) > 32768 || json.Unmarshal([]byte(body), &input) != nil || len(input.Domains)+len(input.CIDRs) > 256 {
		return RuleInputResult{}, errors.New("invalid or oversized rule validation request")
	}
	for field, values := range map[string][]string{"domains": input.Domains, "cidrs": input.CIDRs} {
		for i, value := range values {
			var err error
			if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
				err = errors.New("use one expression per line")
			} else if field == "domains" {
				_, err = NormalizeRuleDomain(value)
			} else {
				_, err = NormalizeRuleIP(value)
			}
			if err != nil {
				return RuleInputResult{Field: field, Index: i, Error: err.Error()}, nil
			}
		}
	}
	return RuleInputResult{Valid: true}, nil
}
