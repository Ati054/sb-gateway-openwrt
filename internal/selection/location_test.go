package selection

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"example.org/resilient-proxy/internal/catalog"
)

func TestCandidatesRespectOrderedCountriesAndStableCity(t *testing.T) {
	nodes := []catalog.Node{
		{ID: "ca", Country: "CA", City: "🇨🇦 ⚡️ Toronto", SelectionIdentity: "0123456789abcdef01234567"},
		{ID: "us", Country: "US", City: "Chicago"},
		{ID: "de", Country: "DE", City: "Berlin"},
	}
	order := []string{"city:CA:" + url.PathEscape("🇨🇦 Toronto"), "country:US", "region:europe"}
	selected, groups, err := Candidates(nodes, order, Filter{})
	if err != nil || !reflect.DeepEqual(selected, []string{"ca", "us", "de"}) || groups["de"] != 2 {
		t.Fatalf("ordered selection = %#v %#v %v", selected, groups, err)
	}
	location, _, err := Candidates(nodes, []string{"location:0123456789abcdef"}, Filter{})
	if err != nil || !reflect.DeepEqual(location, []string{"ca"}) {
		t.Fatalf("stable location = %#v %v", location, err)
	}
}

func TestNoSelectionMeansNoCandidatesAndBadFilterFailsClosed(t *testing.T) {
	nodes := []catalog.Node{{ID: "ca", Country: "CA"}}
	selected, _, err := Candidates(nodes, nil, Filter{})
	if err != nil || len(selected) != 0 {
		t.Fatalf("empty order unexpectedly selected nodes: %#v %v", selected, err)
	}
	for _, invalid := range []string{"country:ca", "region:fake", "city:US:%zz", "location:../etc"} {
		if _, _, err := Candidates(nodes, []string{invalid}, Filter{}); err == nil {
			t.Fatalf("invalid selector accepted: %q", invalid)
		}
	}
}

// ISO territories plus Kosovo, from Unicode CLDR territory containment.
func TestRegionCoverageAndPreviouslyUnassignedCountries(t *testing.T) {
	const countries = "AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS XK YE YT ZA ZM ZW"
	for _, code := range strings.Fields(countries) {
		count := 0
		for _, members := range regions {
			if members[code] {
				count++
			}
		}
		want := 1
		if code == "AQ" {
			want = 0
		}
		if count != want {
			t.Errorf("%s belongs to %d regions; want %d", code, count, want)
		}
	}
	for code, region := range map[string]string{"NG": "africa", "XK": "europe", "IO": "africa", "TF": "africa", "BV": "america", "GS": "america"} {
		nodes := []catalog.Node{{ID: code, Country: code}}
		selected, _, err := Candidates(nodes, []string{"region:" + region}, Filter{})
		if err != nil || !reflect.DeepEqual(selected, []string{code}) {
			t.Errorf("%s missing from %s: %v %v", code, region, selected, err)
		}
		other, _, err := Candidates(nodes, []string{"region:other"}, Filter{})
		if err != nil || len(other) != 0 {
			t.Errorf("%s incorrectly selected as other: %v %v", code, other, err)
		}
	}
}
