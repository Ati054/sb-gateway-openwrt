package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
	"github.com/Ati054/sb-gateway-openwrt/internal/rulesets"
)

// Persist every domain set once. The small snapshot retains references/counts;
// text lists and parsing maps can be collected after a successful publication.
func compactRulesetSnapshot(snapshot rulesets.Snapshot, root string) (rulesets.Snapshot, bool, error) {
	result := snapshot
	result.Version = rulesets.SnapshotVersion
	result.Packs = make(map[string]rulesets.Rules, len(snapshot.Packs))
	result.Countries = make(map[string]rulesets.CountryRules, len(snapshot.Countries))
	changed := snapshot.Version != result.Version
	compact := func(code string, source rulesets.Rules, expressions func() ([]string, error)) (rulesets.Rules, error) {
		if source.Reference != "" {
			return source, geoipasset.VerifyDomain(root, source.Reference)
		}
		if source.Count() == 0 {
			return source, nil
		}
		values, err := expressions()
		if err != nil {
			return rulesets.Rules{}, err
		}
		ref, err := geoipasset.PublishDomains(root, code, values)
		if err != nil {
			return rulesets.Rules{}, err
		}
		changed = true
		return rulesets.Rules{Reference: ref, Entries: source.Count()}, nil
	}
	for id, source := range snapshot.Packs {
		sum := sha256.Sum256([]byte(id))
		compiled, err := compact("service-"+hex.EncodeToString(sum[:12]), source, func() ([]string, error) { return rulesets.PackExpressions(id, snapshot) })
		if err != nil {
			return rulesets.Snapshot{}, false, err
		}
		result.Packs[id] = compiled
	}
	for code, country := range snapshot.Countries {
		compiled, err := compact(code, country.Geosite, func() ([]string, error) { return rulesets.GeoSiteExpressions(country.Geosite), nil })
		if err != nil {
			return rulesets.Snapshot{}, false, err
		}
		country.Geosite = compiled
		result.Countries[code] = country
	}
	return result, changed, result.Validate()
}
func unchangedDomainStatuses(before, after rulesets.Snapshot, statuses []rulesets.PackStatus) {
	for i := range statuses {
		status := &statuses[i]
		if status.State != "updated" {
			continue
		}
		var old, new rulesets.Rules
		if len(status.ID) > 8 && status.ID[:8] == "geosite:" {
			code := status.ID[8:]
			old = before.Countries[code].Geosite
			new = after.Countries[code].Geosite
		} else {
			old = before.Packs[status.ID]
			new = after.Packs[status.ID]
		}
		if old.Reference != "" && old.Reference == new.Reference && old.Entries == new.Entries {
			status.State = "unchanged"
		}
	}
}
