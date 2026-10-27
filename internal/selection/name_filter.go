package selection

import (
	"errors"
	"strings"
	"unicode"
)

type nameTerm struct {
	text  string
	exact bool
}

// Quotes select a full name; separators outside quotes select alternatives.
// Doubled quotes represent a literal quote inside a quoted name.
func parseNameFilter(raw string) ([]nameTerm, error) {
	if len(raw) > 512 {
		return nil, errors.New("node filter is too large")
	}
	if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return nil, errors.New("control character in node filter")
	}
	var terms []nameTerm
	for raw != "" {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			break
		}
		if raw[0] == ',' || raw[0] == '|' {
			raw = raw[1:]
			continue
		}
		if raw[0] != '"' {
			end := strings.IndexAny(raw, ",|")
			if end < 0 {
				end = len(raw)
			}
			if text := normalizeName(raw[:end]); text != "" {
				terms = append(terms, nameTerm{text: text})
			}
			raw = raw[end:]
			continue
		}
		var value strings.Builder
		closed := false
		for i := 1; i < len(raw); i++ {
			if raw[i] != '"' {
				value.WriteByte(raw[i])
				continue
			}
			if i+1 < len(raw) && raw[i+1] == '"' {
				value.WriteByte('"')
				i++
				continue
			}
			raw = strings.TrimSpace(raw[i+1:])
			closed = true
			break
		}
		if !closed {
			return nil, errors.New("close the double quotes around the full node name")
		}
		text := normalizeName(value.String())
		if text == "" {
			return nil, errors.New("quoted node name cannot be empty")
		}
		if raw != "" && raw[0] != ',' && raw[0] != '|' {
			return nil, errors.New("separate node names with a comma or | after a closing quote")
		}
		terms = append(terms, nameTerm{text: text, exact: true})
	}
	return terms, nil
}

func matchesNameTerms(label string, terms []nameTerm, empty bool) bool {
	if len(terms) == 0 {
		return empty
	}
	for _, term := range terms {
		if term.exact && label == term.text || !term.exact && strings.Contains(label, term.text) {
			return true
		}
	}
	return false
}
