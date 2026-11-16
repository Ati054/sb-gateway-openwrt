package xray

import (
	"errors"
	"strings"

	"golang.org/x/net/idna"
)

// NormalizeRuleDomain accepts the same international hostnames as a browser.
// Explicit regex/keyword expressions retain their user-specified contents.
func NormalizeRuleDomain(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 4096 {
		return "", errors.New("domain expression is too large")
	}
	prefix, name := "", value
	if before, after, ok := strings.Cut(value, ":"); ok {
		switch strings.ToLower(before) {
		case "regexp", "keyword":
			value = strings.ToLower(before) + ":" + after
			if !validRuleDomain(value) {
				return "", errors.New("invalid domain expression")
			}
			return value, nil
		case "full", "domain":
			prefix, name = strings.ToLower(before)+":", after
		default:
			return "", errors.New("unknown domain expression")
		}
	}
	ascii, err := idna.Lookup.ToASCII(strings.TrimSuffix(name, "."))
	if err != nil {
		return "", errors.New("invalid international domain name")
	}
	value = prefix + strings.ToLower(ascii)
	if !validRuleDomain(value) {
		return "", errors.New("invalid domain name")
	}
	return value, nil
}
