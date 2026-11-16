package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Ati054/xroute-openwrt/internal/geoipasset"
	"github.com/Ati054/xroute-openwrt/internal/rulesets"
	"github.com/Ati054/xroute-openwrt/internal/xray"
)

func TestAppliedNetworkPreservesOrderedRoutingWithoutSubscriptionSecrets(t *testing.T) {
	paths := Paths{DataRoot: t.TempDir(), RunRoot: t.TempDir()}
	config := captureInputFixture()
	config.Subscriptions[0].URL = "https://source.invalid/subscription-secret"
	config.SubscriptionURL = "https://source.invalid/legacy-secret"
	config.Base.Policies[0].Rules = []xray.RouteRule{
		{ID: "wan", Action: "direct", Domains: []string{"domain:example.com"}},
		{ID: "strict", Action: "proxy", Domains: []string{"domain:private.test"}, ProxyOutageAction: "block"},
	}
	if err := saveAppliedNetwork(paths, config); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(captureRecoveryPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("subscription-secret")) || bytes.Contains(body, []byte("legacy-secret")) {
		t.Fatal("subscription URL saved in network recovery")
	}
	restored, err := readAppliedNetwork(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Base, config.Base) || len(restored.Subscriptions) != len(config.Subscriptions) || restored.Subscriptions[0].ID != config.Subscriptions[0].ID {
		t.Fatal("last applied routing or durable catalog source identity lost")
	}
	if sourceRefreshDue(restored.Subscriptions[0], time.Now(), time.Time{}, true) {
		t.Fatal("recovery can download a subscription")
	}
	if config.Subscriptions[0].URL != "https://source.invalid/subscription-secret" {
		t.Fatal("saving recovery modified the live configuration")
	}
	info, _ := os.Stat(captureRecoveryPath(paths))
	if info.Mode().Perm() != 0600 {
		t.Fatal("recovery file is not private")
	}
}

func TestAppliedNetworkCannotMixCaptureGenerations(t *testing.T) {
	paths := Paths{DataRoot: t.TempDir(), RunRoot: t.TempDir()}
	old := captureInputFixture()
	if err := saveAppliedNetwork(paths, old); err != nil {
		t.Fatal(err)
	}
	next := old
	next.Capture.DirectIPs = []string{"198.51.100.1/32"}
	if err := saveCaptureRecovery(paths, next); err != nil {
		t.Fatal(err)
	}
	if _, err := readAppliedNetwork(paths); err == nil {
		t.Fatal("half-written recovery reused a different routing generation")
	}
	if err := saveAppliedNetwork(paths, next); err != nil {
		t.Fatal(err)
	}
	if _, err := readAppliedNetwork(paths); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveredCoreKeepsResolvedDomainsWhenRulesetCacheIsLost(t *testing.T) {
	paths := Paths{DataRoot: t.TempDir(), RunRoot: t.TempDir()}
	config := captureInputFixture()
	config.Base.Policies[0].Rules = []xray.RouteRule{{ID: "local-sites", Action: "direct", Country: "ru", UseGeosite: true, ProxyOutageAction: "block"}}
	snapshot := rulesets.Snapshot{Version: rulesets.SnapshotVersion, Countries: map[string]rulesets.CountryRules{"ru": {Geosite: rulesets.Rules{Suffix: []string{"direct.lab"}}}}}
	assets := filepath.Join(paths.DataRoot, "geoip-assets")
	resolved, err := withRulesets(config.Base, config.Policies, snapshot, assets)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Policies[0].Rules[0].CountryDomains) != 1 {
		t.Fatal("fixture did not compile downloaded domains")
	}
	ref := resolved.Policies[0].Rules[0].CountryDomains[0]
	if err := saveAppliedNetwork(paths, config, resolved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulesetSnapshotPath(paths), []byte("broken future cache"), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := readAppliedNetwork(paths)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		base, err := runtimeRoutingBase(recovered, loadRulesetSnapshot(paths), assets, true)
		if err != nil || !reflect.DeepEqual(base, resolved) {
			t.Fatal("core recovery rebuilt the acknowledged rules from a different cache", base, err)
		}
		recovered.Base = base
	}
	name, ok := geoipasset.DomainReferenceName(ref)
	if !ok {
		t.Fatal("domain fixture did not use an immutable asset", ref)
	}
	if err := os.Chtimes(filepath.Join(assets, name), time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := geoipasset.Prune(assets, []string{captureRecoveryPath(paths)}, nil, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := geoipasset.VerifyDomain(assets, ref); err != nil {
		t.Fatal("last working domain asset was pruned", err)
	}
}
