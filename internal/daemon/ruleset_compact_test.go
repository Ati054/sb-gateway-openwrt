package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
	"github.com/Ati054/sb-gateway-openwrt/internal/rulesets"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
	"github.com/xtls/xray-core/infra/conf/serial"
	"maps"
	"os"
	"path/filepath"
	"reflect"
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
	// Exercise application, not just asset publication: aggregate cards can
	// contain both their own compact reference and references to dependencies.
	for _, choice := range choices {
		t.Run(choice.ID, func(t *testing.T) {
			base := xray.DefaultBaseConfig()
			base.DefaultPolicy = "one"
			base.Policies = []xray.BasePolicy{{ID: "one", Rules: []xray.RouteRule{{ID: "services", Action: "direct", ServicePacks: []string{choice.ID}}}}}
			compiled, err := withRulesets(base, []settings.Policy{{ID: "one"}}, compact, root)
			if err != nil {
				t.Fatal("apply compact card", err)
			}
			for _, ref := range compiled.Policies[0].Rules[0].ServiceDomains {
				if err := geoipasset.VerifyDomain(root, ref); err != nil {
					t.Fatal("card did not retain a valid shared asset", err)
				}
			}
		})
	}
	// Countries, overlapping aggregate selections and mixed old/new snapshots
	// must compile to the same routing and DNS references after restart/update.
	snapshot.Countries = map[string]rulesets.CountryRules{}
	base := xray.DefaultBaseConfig()
	base.DefaultPolicy = "one"
	base.Policies = []xray.BasePolicy{{ID: "one", Rules: []xray.RouteRule{{ID: "services", Action: "direct", ServicePacks: append(ids, ids...)}}}}
	for _, country := range []string{"ru", "cn", "ir"} {
		snapshot.Countries[country] = rulesets.CountryRules{Geosite: rulesets.Rules{Suffix: []string{"example." + country}, Exact: []string{"exact." + country}, Keyword: []string{"needle-" + country}, Regex: []string{`^regex\.` + country + `$`}}, GeoIP: []string{"198.51.100.0/24", "2001:db8::/32"}}
		base.Policies[0].Rules = append(base.Policies[0].Rules, xray.RouteRule{ID: "country-" + country, Action: "proxy", Country: country, UseGeosite: true, UseGeoIP: true})
	}
	compact, _, err = compactRulesetSnapshot(snapshot, root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rulesets.json")
	if err := rulesets.Save(path, compact); err != nil {
		t.Fatal(err)
	}
	restored, err := rulesets.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	mixed := restored
	mixed.Packs = maps.Clone(restored.Packs)
	for i, id := range ids {
		if raw, ok := snapshot.Packs[id]; ok && i%2 == 0 {
			mixed.Packs[id] = raw
		}
	}
	t.Setenv("XRAY_LOCATION_ASSET", root)
	var expected []xray.BasePolicy
	for _, stage := range []struct {
		name string
		data rulesets.Snapshot
	}{{"raw", snapshot}, {"compact", compact}, {"restored", restored}, {"mixed", mixed}} {
		t.Run(stage.name, func(t *testing.T) {
			compiled, err := withRulesets(base, []settings.Policy{{ID: "one"}}, stage.data, root)
			if err != nil {
				t.Fatal(err)
			}
			if expected == nil {
				expected = compiled.Policies
			} else if !reflect.DeepEqual(expected, compiled.Policies) {
				t.Fatal("routing changed with snapshot storage format")
			}
			for _, rule := range compiled.Policies[0].Rules {
				seen := map[string]bool{}
				for _, ref := range rule.ServiceDomains {
					if seen[ref] {
						t.Fatal("overlapping cards duplicated component", ref)
					}
					seen[ref] = true
				}
			}
			body, err := xray.RenderBase(compiled)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := serial.LoadJSONConfig(bytes.NewReader(body)); err != nil {
				t.Fatal("Xray rejected shared country/service assets", err)
			}
			dnsPolicies := dnsPoliciesFromBase(compiled)
			if len(dnsPolicies) != 1 || len(dnsPolicies[0].Rules) != len(compiled.Policies[0].Rules) {
				t.Fatal("DNS rules lost during aggregate expansion")
			}
			for i, rule := range dnsPolicies[0].Rules {
				if rule.Action != compiled.Policies[0].Rules[i].Action {
					t.Fatal("DNS action changed")
				}
			}
		})
	}
}
