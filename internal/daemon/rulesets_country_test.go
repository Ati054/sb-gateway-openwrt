package daemon

import (
	"context"
	"testing"

	"example.org/resilient-proxy/internal/rulesets"
	"example.org/resilient-proxy/internal/settings"
	"example.org/resilient-proxy/internal/xray"
)

func TestWithRulesetsCompilesOnlySelectedCountryKinds(t *testing.T) {
	base := xray.DefaultBaseConfig()
	base.Policies[0].Rules = []xray.RouteRule{{ID: "country-ru", Action: "direct",
		Country: "ru", UseGeosite: true, UseGeoIP: false}}
	snapshot := rulesets.Snapshot{Version: rulesets.SnapshotVersion, Countries: map[string]rulesets.CountryRules{
		"ru": {Geosite: rulesets.Rules{Suffix: []string{"ru", "example.ru"}},
			GeoIP: []string{"203.0.113.0/24"}},
	}}
	compiled, err := withRulesets(base, []settings.Policy{{ID: "main"}}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	rule := compiled.Policies[0].Rules[0]
	if len(rule.CountryDomains) != 2 || len(rule.CountryCIDRs) != 0 ||
		rule.CountryDomains[0] != "domain:ru" {
		t.Fatalf("unselected GeoIP leaked or GeoSite missing: %+v", rule)
	}
	if len(base.Policies[0].Rules[0].CountryDomains) != 0 {
		t.Fatal("source config mutated while compiling live rules")
	}
}

func TestNoSelectedCountriesRemoveOldSnapshot(t *testing.T) {
	paths := Paths{DataRoot: t.TempDir(), RunRoot: t.TempDir()}
	previous := rulesets.Snapshot{Version: rulesets.SnapshotVersion, Countries: map[string]rulesets.CountryRules{
		"ru": {Geosite: rulesets.Rules{Suffix: []string{"example.ru"}}},
	}}
	if err := rulesets.Save(rulesetSnapshotPath(paths), previous); err != nil {
		t.Fatal(err)
	}
	runRulesetUpdates(context.Background(), settings.Config{}, paths, nil, previous, xray.BaseConfig{}, nil)
	current, err := rulesets.Load(rulesetSnapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Packs) != 0 || len(current.Countries) != 0 {
		t.Fatalf("unselected country data remained on flash: %+v", current)
	}
}
