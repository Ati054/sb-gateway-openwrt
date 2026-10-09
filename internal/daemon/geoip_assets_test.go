package daemon

import (
	"bytes"
	"encoding/json"
	"example.org/resilient-proxy/internal/geoipasset"
	"example.org/resilient-proxy/internal/rulesets"
	"example.org/resilient-proxy/internal/settings"
	"example.org/resilient-proxy/internal/xray"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenWrtCountryRulesShareImmutableBinaryGeoIP(t *testing.T) {
	root := t.TempDir()
	base := xray.DefaultBaseConfig()
	base.Policies = []xray.BasePolicy{{ID: "one", Rules: []xray.RouteRule{{ID: "a", Action: "direct", Country: "ru", UseGeoIP: true}}}, {ID: "two", Rules: []xray.RouteRule{{ID: "b", Action: "proxy", Country: "ru", UseGeoIP: true}}}}
	base.DefaultPolicy = "one"
	snap := rulesets.Snapshot{Version: rulesets.SnapshotVersion, Countries: map[string]rulesets.CountryRules{"ru": {GeoIP: []string{"198.51.100.0/24", "2001:db8::/32"}}}}
	got, err := withRulesets(base, []settings.Policy{{ID: "one"}, {ID: "two"}}, snap, root)
	if err != nil {
		t.Fatal(err)
	}
	ref := got.Policies[0].Rules[0].CountryGeoIP
	if ref == "" || ref != got.Policies[1].Rules[0].CountryGeoIP || len(got.Policies[0].Rules[0].CountryCIDRs) != 0 {
		t.Fatal("country rules duplicate prefix lists")
	}
	if err := geoipasset.Verify(root, ref); err != nil {
		t.Fatal(err)
	}
	body, err := xray.RenderBase(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("198.51.100.0/24")) || !bytes.Contains(body, []byte(ref)) {
		t.Fatal("rendered routing did not reference binary database")
	}
	snap.Countries["ru"] = rulesets.CountryRules{GeoIP: []string{"203.0.113.0/24"}}
	next, err := withRulesets(base, []settings.Policy{{ID: "one"}, {ID: "two"}}, snap, root)
	if err != nil {
		t.Fatal(err)
	}
	if next.Policies[0].Rules[0].CountryGeoIP == ref {
		t.Fatal("GeoIP update overwrote immutable identity")
	}
	if err := geoipasset.Verify(root, ref); err != nil {
		t.Fatal("rollback asset lost", err)
	}
	paths := Paths{RunRoot: root, DataRoot: root}
	if err := retainGeoIPAssets(paths, got, next); err != nil {
		t.Fatal(err)
	}
	var docs struct {
		Configurations []json.RawMessage `json:"configurations"`
	}
	if err := func() error {
		body, err := os.ReadFile(filepath.Join(root, "geoip-retention.json"))
		if err != nil {
			return err
		}
		return json.Unmarshal(body, &docs)
	}(); err != nil || len(docs.Configurations) != 2 {
		t.Fatal("retention omitted active/rollback", err)
	}
}

func TestGeoIPRetentionDocumentProtectsReferencedAssetsDuringPrune(t *testing.T) {
	root := t.TempDir()
	paths := Paths{RunRoot: root, DataRoot: root}
	assets := filepath.Join(root, "geoip-assets")
	pack := []byte(`{"version":3,"rules":[{"ip_cidr":["198.51.100.0/24"]}]}`)
	ref, err := geoipasset.Publish(assets, "geoip-ru", pack)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := geoipasset.Publish(assets, "geoip-ru", []byte(`{"version":3,"rules":[{"ip_cidr":["203.0.113.0/24"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	base := xray.DefaultBaseConfig()
	base.Policies[0].Rules = []xray.RouteRule{{ID: "geo", Action: "direct", Country: "ru", UseGeoIP: true, CountryGeoIP: ref}}
	if err := retainGeoIPAssets(paths, base); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{ref, stale} {
		name, _ := geoipasset.ReferenceName(v)
		if err := os.Chtimes(filepath.Join(assets, name), time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := geoipasset.Prune(assets, []string{filepath.Join(root, "geoip-retention.json")}, nil, nil, time.Now())
	if err != nil || removed != 1 {
		t.Fatalf("retention prevented safe cleanup: removed=%d err=%v", removed, err)
	}
	if err := geoipasset.Verify(assets, ref); err != nil {
		t.Fatal("active GeoIP removed", err)
	}
}
