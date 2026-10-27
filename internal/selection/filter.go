package selection

import (
	"errors"
	"strings"
	"unicode"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

// Filter restricts live catalog membership; it never changes node order.
// Comma-separated name fragments are alternatives. Exclusions take precedence.
type Filter struct {
	Subscription string   `json:"subscription"`
	Countries    []string `json:"countries"`
	Protocols    []string `json:"protocols"`
	Include      string   `json:"include"`
	Exclude      string   `json:"exclude"`
}

func (f Filter) Validate() error {
	if len(f.Subscription) > 128 || len(f.Countries) > 256 || len(f.Protocols) > 16 || len(f.Include) > 512 || len(f.Exclude) > 512 {
		return errors.New("node filter is too large")
	}
	for _, country := range f.Countries {
		if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			return errors.New("invalid node-filter country")
		}
	}
	for _, protocol := range f.Protocols {
		switch protocol {
		case "vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "socks", "http":
		default:
			return errors.New("invalid node-filter protocol")
		}
	}
	for _, raw := range []string{f.Subscription, f.Include, f.Exclude} {
		for _, r := range raw {
			if unicode.IsControl(r) {
				return errors.New("control character in node filter")
			}
		}
	}
	for _, raw := range []string{f.Include, f.Exclude} {
		if _, err := parseNameFilter(raw); err != nil {
			return err
		}
	}
	return nil
}

// Replacer is immutable and safe to share across concurrent catalog readers.
var nameVariationReplacer = strings.NewReplacer("\ufe0e", "", "\ufe0f", "")

func normalizeName(value string) string {
	return strings.ToLower(strings.TrimSpace(nameVariationReplacer.Replace(value)))
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func (f Filter) Matches(node catalog.Node) bool {
	include, err := parseNameFilter(f.Include)
	if err != nil {
		return false
	}
	exclude, err := parseNameFilter(f.Exclude)
	return err == nil && f.matchesNames(node, include, exclude)
}

func (f Filter) matchesNames(node catalog.Node, include, exclude []nameTerm) bool {
	if f.Subscription != "" && f.Subscription != "*" && f.Subscription != node.ProviderID {
		return false
	}
	if len(f.Countries) > 0 && !contains(f.Countries, strings.ToUpper(node.Country)) {
		return false
	}
	if len(f.Protocols) > 0 && !contains(f.Protocols, strings.ToLower(node.Protocol)) {
		return false
	}
	if f.Include == "" && f.Exclude == "" {
		return true
	}
	label := normalizeName(node.Label)
	return matchesNameTerms(label, include, true) && !matchesNameTerms(label, exclude, false)
}
