package subscription

import (
	"strings"
	"unicode"
)

// Labels are display text, not markup. Bound remote input without damaging
// ordinary Unicode names or emoji. HTML remains literal and is escaped by UI.
func cleanLabel(label string) string {
	var result strings.Builder
	count := 0
	for _, r := range label {
		if unicode.IsControl(r) || r == '\u200e' || r == '\u200f' || (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') {
			continue
		}
		if count == 256 {
			break
		}
		result.WriteRune(r)
		count++
	}
	return strings.TrimSpace(result.String())
}
