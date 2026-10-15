package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
	"github.com/Ati054/sb-gateway-openwrt/internal/rulesets"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
	"os"
	"strings"
	"testing"
)

func TestAllCountriesAndServiceSetsShareCompactAssets(t *testing.T) {
	root := t.TempDir()
	snapshot := rulesets.Snapshot{Version: 2, Packs: map[string]rulesets.Rules{"youtube": {Suffix: []string{"youtube.com"}}, "discord": {Suffix: []string{"discord.com"}}}, Countries: map[string]rulesets.CountryRules{}}
	var groups []xray.RouteRule
	for _, country := range []string{"ru", "cn", "ir"} {
		snapshot.Countries[country] = rulesets.CountryRules{Geosite: rulesets.Rules{Suffix: []string{"example." + country}}}
		groups = append(groups, xray.RouteRule{ID: "country-" + country, Action: "direct", Country: country, UseGeosite: true})
	}
	groups = append(groups, xray.RouteRule{ID: "services", Action: "direct", ServicePacks: []string{"youtube", "discord"}})
	compact, changed, err := compactRulesetSnapshot(snapshot, root)
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	for _, country := range []string{"ru", "cn", "ir"} {
		c := compact.Countries[country].Geosite
		if c.Reference == "" || c.Count() != 1 || len(c.Suffix) != 0 {
			t.Fatal(country, c)
		}
	}
	base := xray.DefaultBaseConfig()
	base.DefaultPolicy = "one"
	base.Policies = []xray.BasePolicy{{ID: "one", Rules: groups}, {ID: "two", Rules: append([]xray.RouteRule(nil), groups...)}}
	// A different combination must still reuse YouTube's one file/matcher.
	base.Policies[1].Rules[len(groups)-1].ServicePacks = []string{"youtube"}
	compiled, err := withRulesets(base, []settings.Policy{{ID: "one"}, {ID: "two"}}, compact, root)
	if err != nil {
		t.Fatal(err)
	}
	a := compiled.Policies[0].Rules[len(groups)-1].ServiceDomains
	b := compiled.Policies[1].Rules[len(groups)-1].ServiceDomains
	if len(a) != 2 || len(b) != 1 || !strings.Contains(strings.Join(a, " "), b[0]) {
		t.Fatal("service set duplicated across combinations", a, b)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 5 {
		t.Fatal("expected 3 countries and 2 service files", len(entries), err)
	}
	again, changed, err := compactRulesetSnapshot(compact, root)
	if err != nil || changed || again.Packs["youtube"].Reference != compact.Packs["youtube"].Reference {
		t.Fatal("compact snapshot was recompiled", err, changed)
	}
}

// Include every internal component, not only top-level cards. A parent's
// immutable file must contain its own data; its children have separate files.
func TestCompactAllSelectableCardsAndInternalDependencies(t *testing.T) {
	choices, err := rulesets.Featured()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(choices))
	for _, choice := range choices {
		ids = append(ids, choice.ID)
	}
	snapshot, _, err := rulesets.Build(context.Background(), ids, rulesets.Snapshot{}, func(_ context.Context, name string) (string, error) {
		sum := sha256.Sum256([]byte(name))
		return fmt.Sprintf("domain:pack-%x.example", sum[:8]), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ozon", "ru-retail"} {
		if snapshot.Packs[id].Count() == 0 {
			t.Fatalf("fixture missing internal component %s", id)
		}
	}
	root := t.TempDir()
	compact, changed, err := compactRulesetSnapshot(snapshot, root)
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	if len(compact.Packs) != len(snapshot.Packs) {
		t.Fatal("stored components lost")
	}
	for id, raw := range snapshot.Packs {
		before, err := rulesets.PackExpressions(id, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		got := compact.Packs[id]
		if got.Reference == "" || got.Count() != raw.Count() {
			t.Fatal("component not compacted", id)
		}
		sum := sha256.Sum256([]byte(id))
		expected, err := geoipasset.PublishDomains(root, fmt.Sprintf("service-%x", sum[:12]), before)
		if err != nil || expected != got.Reference {
			t.Fatalf("component changed: %s: %v", id, err)
		}
	}
	if _, changed, err := compactRulesetSnapshot(compact, root); err != nil || changed {
		t.Fatal("repeat migration", err, changed)
	}
}
