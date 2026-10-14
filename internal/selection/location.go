// Package selection applies the ordered location filters used by the
// client-side SB Gateway policy contract to the OpenWrt catalog.
package selection

import (
	"errors"
	"net/url"
	"strings"
	"unicode"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

// Region membership is adapted from SB Gateway 1.6.21
// internal/runtimeconfig/xray_source_outbounds.go, completed for ISO territories
// and Kosovo using Unicode CLDR territory containment. Cyprus stays in Europe.
var regions = map[string]map[string]bool{
	"europe":  countrySet("AD AL AT AX BA BE BG BY CH CY CZ DE DK EE ES FI FO FR GB GG GI GR HR HU IE IM IS IT JE LI LT LU LV MC MD ME MK MT NL NO PL PT RO RS RU SE SI SJ SK SM UA VA XK"),
	"asia":    countrySet("AE AF AM AZ BD BH BN BT CN GE HK ID IL IN IQ IR JO JP KG KH KP KR KW KZ LA LB LK MM MN MO MV MY NP OM PH PK PS QA SA SG SY TH TJ TL TM TR TW UZ VN YE"),
	"america": countrySet("AG AI AR AW BB BL BM BO BQ BR BS BV BZ CA CL CO CR CU CW DM DO EC FK GD GF GL GP GS GT GY HN HT JM KN KY LC MF MQ MS MX NI PA PE PM PR PY SR SV SX TC TT US UY VC VE VG VI"),
	"africa":  countrySet("AO BF BI BJ BW CD CF CG CI CM CV DJ DZ EG EH ER ET GA GH GM GN GQ GW IO KE KM LR LS LY MA MG ML MR MU MW MZ NA NE NG RE RW SC SD SH SL SN SO SS ST SZ TD TF TG TN TZ UG YT ZA ZM ZW"),
	"oceania": countrySet("AS AU CC CK CX FJ FM GU HM KI MH MP NC NF NR NU NZ PF PG PN PW SB TK TO TV UM VU WF WS"),
}

func countrySet(raw string) map[string]bool {
	result := make(map[string]bool)
	for _, value := range strings.Fields(raw) {
		result[value] = true
	}
	return result
}

// Candidates returns every matching node in filter order, with its group
// index. The health controller limits the working shortlist, never inventory.
func Candidates(nodes []catalog.Node, order []string, filter Filter) ([]string, map[string]int, error) {
	if err := filter.Validate(); err != nil {
		return nil, nil, err
	}
	result := make([]string, 0)
	groups := make(map[string]int)
	claimed := make(map[string]bool)
	for group, selector := range order {
		if err := validateSelector(selector); err != nil {
			return nil, nil, err
		}
		for _, node := range nodes {
			if !claimed[node.ID] && filter.Matches(node) && matches(selector, node) {
				claimed[node.ID] = true
				groups[node.ID] = group
				result = append(result, node.ID)
			}
		}
	}
	return result, groups, nil
}

func validateSelector(selector string) error {
	kind, value, found := strings.Cut(selector, ":")
	if !found || len(selector) > 768 {
		return errors.New("invalid location selector")
	}
	switch kind {
	case "all":
		if value == "*" {
			return nil
		}
	case "subscription":
		decoded, err := url.PathUnescape(value)
		if err == nil && decoded != "" && len(decoded) <= 128 && !strings.ContainsAny(decoded, "\r\n\x00") {
			return nil
		}
	case "country":
		if len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z' {
			return nil
		}
	case "region":
		if value == "other" || regions[value] != nil {
			return nil
		}
	case "city":
		country, city, ok := strings.Cut(value, ":")
		decoded, err := url.PathUnescape(city)
		if ok && err == nil && (country == "*" || (len(country) == 2 && country == strings.ToUpper(country))) && normalizeCity(decoded) != "" {
			return nil
		}
	case "location":
		if len(value) == 16 || len(value) == 24 {
			for _, character := range value {
				if !strings.ContainsRune("0123456789abcdef", character) {
					return errors.New("invalid location selector")
				}
			}
			return nil
		}
	}
	return errors.New("invalid location selector")
}

func matches(selector string, node catalog.Node) bool {
	kind, value, _ := strings.Cut(selector, ":")
	country := strings.ToUpper(node.Country)
	switch kind {
	case "all":
		return value == "*"
	case "subscription":
		decoded, _ := url.PathUnescape(value)
		return node.ProviderID == decoded
	case "country":
		return country == value
	case "region":
		if value != "other" {
			return regions[value][country]
		}
		for _, countries := range regions {
			if countries[country] {
				return false
			}
		}
		return true
	case "city":
		wantedCountry, encoded, _ := strings.Cut(value, ":")
		city, _ := url.PathUnescape(encoded)
		return (wantedCountry == "*" || wantedCountry == country) && strings.EqualFold(normalizeCity(node.City), normalizeCity(city))
	case "location":
		return strings.HasPrefix(node.SelectionIdentity, value)
	}
	return false
}

func normalizeCity(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	value = strings.TrimLeftFunc(value, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsNumber(character)
	})
	if value == "" || len(value) > 256 {
		return ""
	}
	for _, character := range value {
		if character < 32 {
			return ""
		}
	}
	return value
}
