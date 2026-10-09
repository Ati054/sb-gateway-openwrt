package selection

import (
	"errors"
	"strings"
	"unicode"

	"example.org/resilient-proxy/internal/catalog"
)

// Filter restricts live catalog membership; it never changes node order.
// Name matching follows SB Gateway: all name fragments, any excluded word.
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
	for _, part := range strings.Split(f.Exclude, ",") {
		word := normalizeName(part)
		for _, r := range word {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return errors.New("excluded node names must be whole words separated by commas")
			}
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
	for _, part := range strings.Split(f.Include, ",") {
		if token := normalizeName(part); token != "" && !strings.Contains(label, token) {
			return false
		}
	}
	if strings.TrimSpace(f.Exclude) == "" {
		return true
	}
	words := strings.FieldsFunc(label, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, part := range strings.Split(f.Exclude, ",") {
		if word := normalizeName(part); word != "" && contains(words, word) {
			return false
		}
	}
	return true
}
