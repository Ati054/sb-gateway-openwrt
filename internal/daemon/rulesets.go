package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/dnsproxy"
	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
	"github.com/Ati054/sb-gateway-openwrt/internal/resources"
	"github.com/Ati054/sb-gateway-openwrt/internal/rulesets"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
	"github.com/Ati054/sb-gateway-openwrt/internal/subscription"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

type rulesetStatus struct {
	CheckedAt     time.Time             `json:"checked_at"`
	ActiveUpdated time.Time             `json:"active_updated"`
	Packs         []rulesets.PackStatus `json:"packs"`
	Error         string                `json:"error,omitempty"`
}

func rulesetSnapshotPath(paths Paths) string {
	return filepath.Join(paths.DataRoot, "rulesets.json")
}

func loadRulesetSnapshot(paths Paths) rulesets.Snapshot {
	snapshot, err := rulesets.Load(rulesetSnapshotPath(paths))
	if err == nil {
		return snapshot
	}
	if !errors.Is(err, os.ErrNotExist) {
		log.Printf("resproxy: invalid ruleset snapshot ignored; downloaded data unavailable: %v", err)
	}
	return rulesets.Snapshot{Version: rulesets.SnapshotVersion, Packs: map[string]rulesets.Rules{}}
}

func withRulesets(base xray.BaseConfig, policies []settings.Policy, snapshot rulesets.Snapshot, assetRoots ...string) (xray.BaseConfig, error) {
	assetRoot := ""
	if len(assetRoots) > 0 {
		assetRoot = assetRoots[0]
	}
	refs := make(map[string]string)
	countryDomains := make(map[string][]string)
	serviceDomains := make(map[string][]string)
	base.Policies = append([]xray.BasePolicy(nil), base.Policies...)
	for index := range base.Policies {
		if index >= len(policies) || base.Policies[index].ID != policies[index].ID {
			return xray.BaseConfig{}, errors.New("route-list configuration is inconsistent")
		}
		base.Policies[index].Rules = append([]xray.RouteRule(nil), base.Policies[index].Rules...)
		for ruleIndex := range base.Policies[index].Rules {
			rule := &base.Policies[index].Rules[ruleIndex]
			selectedPacks, err := rulesets.Components(rule.ServicePacks)
			if err != nil {
				return xray.BaseConfig{}, err
			}
			var serviceExpressions []string
			for _, packID := range selectedPacks {
				expressions, found := serviceDomains[packID]
				if !found {
					if ref := snapshot.Packs[packID].Reference; ref != "" && assetRoot != "" {
						if err := geoipasset.VerifyDomain(assetRoot, ref); err != nil {
							return xray.BaseConfig{}, err
						}
					}
					var err error
					expressions, err = rulesets.PackExpressions(packID, snapshot)
					if err != nil {
						return xray.BaseConfig{}, err
					}
					if assetRoot != "" && len(expressions) > 0 && snapshot.Packs[packID].Reference == "" {
						sum := sha256.Sum256([]byte(packID))
						ref, err := geoipasset.PublishDomains(assetRoot, "service-"+hex.EncodeToString(sum[:12]), expressions)
						if err != nil {
							return xray.BaseConfig{}, err
						}
						expressions = []string{ref}
					}
					serviceDomains[packID] = expressions
				}
				serviceExpressions = append(serviceExpressions, expressions...)
			}
			serviceIPs, err := rulesets.ServiceCIDRs(rule.ServicePacks)
			if err != nil {
				return xray.BaseConfig{}, err
			}
			rule.CIDRs = append(append([]string(nil), rule.CIDRs...), serviceIPs...)
			rule.ServiceDomains = serviceExpressions
			if rule.Country == "" {
				continue
			}
			country := snapshot.Countries[rule.Country]
			if rule.UseGeosite {
				expressions, found := countryDomains[rule.Country]
				if !found {
					if ref := country.Geosite.Reference; ref != "" && assetRoot != "" {
						if err := geoipasset.VerifyDomain(assetRoot, ref); err != nil {
							return xray.BaseConfig{}, err
						}
					}
					expressions = rulesets.GeoSiteExpressions(country.Geosite)
					if assetRoot != "" && len(expressions) > 0 && country.Geosite.Reference == "" {
						ref, err := geoipasset.PublishDomains(assetRoot, rule.Country, expressions)
						if err != nil {
							return xray.BaseConfig{}, err
						}
						expressions = []string{ref}
					}
					countryDomains[rule.Country] = expressions
				}
				rule.CountryDomains = expressions
			}
			if rule.UseGeoIP {
				if assetRoot != "" && len(country.GeoIP) > 0 {
					ref := refs[rule.Country]
					if ref == "" {
						pack, err := json.Marshal(map[string]any{"version": 3, "rules": []any{map[string]any{"ip_cidr": country.GeoIP}}})
						if err != nil {
							return xray.BaseConfig{}, err
						}
						ref, err = geoipasset.Publish(assetRoot, "geoip-"+rule.Country, pack)
						if err != nil {
							return xray.BaseConfig{}, err
						}
						refs[rule.Country] = ref
					}
					rule.CountryGeoIP = ref
					rule.CountryCIDRs = nil
				} else {
					rule.CountryCIDRs = country.GeoIP
				}

			}
		}
	}
	return base, base.Validate()
}

func runRulesetUpdates(ctx context.Context, config settings.Config, paths Paths, manager *xray.Manager, initial rulesets.Snapshot, initialBase xray.BaseConfig, updateDNS func([]dnsproxy.Policy) error) {
	selectedByPolicy := make([][]string, 0, len(config.Policies))
	for _, policy := range config.Base.Policies {
		for _, rule := range policy.Rules {
			selectedByPolicy = append(selectedByPolicy, rule.ServicePacks)
		}
	}
	selected := rulesets.SelectedFromPolicies(selectedByPolicy)
	countries := map[string]rulesets.CountrySelection{}
	for _, policy := range config.Base.Policies {
		for _, rule := range policy.Rules {
			if rule.Country == "" || (!rule.UseGeosite && !rule.UseGeoIP) {
				continue
			}
			choice := countries[rule.Country]
			choice.Geosite = choice.Geosite || rule.UseGeosite
			choice.GeoIP = choice.GeoIP || rule.UseGeoIP
			countries[rule.Country] = choice
		}
	}
	if len(selected) == 0 && len(countries) == 0 {
		// The configuration no longer references any downloaded lists. Do not
		// retain their previous snapshot on flash or in the running process.
		if len(initial.Packs) > 0 || len(initial.Countries) > 0 {
			if err := rulesets.Save(rulesetSnapshotPath(paths), rulesets.Snapshot{
				Version: rulesets.SnapshotVersion,
				Packs:   map[string]rulesets.Rules{},
			}); err != nil {
				log.Printf("resproxy: unused ruleset snapshot could not be removed: %v", err)
			}
		}
		return
	}
	current, activeBase := initial, initialBase
	requestPath := filepath.Join(paths.RunRoot, "rulesets-refresh.request")
	statusPath := filepath.Join(paths.RunRoot, "rulesets-status.json")
	_ = os.Remove(requestPath)
	delay := 120 * time.Second
	if len(countries) > 0 {
		delay = 5 * time.Second
	}
	initialTimer := time.NewTimer(delay)
	defer initialTimer.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	next := time.Now().Add(delay)
	var lastRequest time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-initialTimer.C:
		case <-ticker.C:
		}
		requested := false
		if info, err := os.Stat(requestPath); err == nil && info.ModTime().After(lastRequest) {
			lastRequest = info.ModTime()
			requested = true
		}
		if !requested && (config.RulesetUpdateMode != "automatic" || time.Now().Before(next)) {
			continue
		}
		next = time.Now().Add(time.Duration(config.RulesetUpdateSeconds) * time.Second)
		release, leaseErr := resources.BeginUpdate(ctx)
		if leaseErr != nil {
			return
		}
		candidate, packStatuses, err := fetchSelectedRulesets(ctx, selected, countries, current, config.Base.HTTPPort)
		if err == nil {
			candidate, _, err = compactRulesetSnapshot(candidate, filepath.Join(paths.DataRoot, "geoip-assets"))
			if err == nil {
				unchangedDomainStatuses(current, candidate, packStatuses)
			}
		}
		for _, pack := range packStatuses {
			if pack.Error != "" && pack.Entries == 0 {
				next = time.Now().Add(5 * time.Minute)
				break
			}
		}
		coreLifecycleMu.RLock()
		status := rulesetStatus{CheckedAt: time.Now().UTC(), ActiveUpdated: current.UpdatedAt, Packs: packStatuses}
		if err == nil {
			previousBase := activeBase
			published := false
			dnsUpdated := false
			var candidateBase xray.BaseConfig
			candidateBase, err = withRulesets(config.Base, config.Policies, candidate, filepath.Join(paths.DataRoot, "geoip-assets"))
			if err == nil && !reflect.DeepEqual(activeBase.Policies, candidateBase.Policies) {
				err = retainGeoIPAssets(paths, activeBase, candidateBase)
				if err == nil {
					err = manager.PublishRouting(ctx, activeBase, candidateBase)
				}
				if err == nil {
					activeBase = candidateBase
					published = true
					if updateDNS != nil {
						if dnsErr := updateDNS(dnsPoliciesFromBase(candidateBase)); dnsErr != nil {
							if rollbackErr := manager.PublishRouting(ctx, candidateBase, previousBase); rollbackErr != nil {
								err = fmt.Errorf("DNS update failed and Xray routing rollback is uncertain: %v; %w", dnsErr, rollbackErr)
							} else {
								activeBase = previousBase
								published = false
								err = dnsErr
							}
						} else {
							dnsUpdated = true
						}
					}
				}
			}
			if err == nil {
				if !reflect.DeepEqual(current.Packs, candidate.Packs) ||
					!reflect.DeepEqual(current.Countries, candidate.Countries) {
					if saveErr := rulesets.Save(rulesetSnapshotPath(paths), candidate); saveErr != nil {
						var rollbackErr error
						if published {
							rollbackErr = manager.PublishRouting(ctx, activeBase, previousBase)
						}
						if rollbackErr == nil && dnsUpdated {
							rollbackErr = updateDNS(dnsPoliciesFromBase(previousBase))
						}
						if rollbackErr != nil {
							err = fmt.Errorf("ruleset persistence failed and routing rollback is uncertain: %v; %w", saveErr, rollbackErr)
						} else {
							activeBase = previousBase
							err = saveErr
						}
					} else {
						current = candidate
					}
				}
			}
		}
		coreLifecycleMu.RUnlock()
		if err != nil {
			status.Error = err.Error()
			log.Printf("resproxy: service-pack update preserved previous routing: %v", err)
			// Retry transient failure sooner while avoiding a busy download loop.
			if config.RulesetUpdateMode == "automatic" {
				next = time.Now().Add(30 * time.Minute)
			}
		}
		status.ActiveUpdated = current.UpdatedAt
		if body, marshalErr := json.Marshal(status); marshalErr == nil {
			if writeErr := os.WriteFile(statusPath, body, 0o600); writeErr != nil {
				log.Printf("resproxy: ruleset status unavailable: %v", writeErr)
			}
		}
		release()
	}
}

func fetchSelectedRulesets(ctx context.Context, selected []string, countries map[string]rulesets.CountrySelection, current rulesets.Snapshot, proxyPort int) (rulesets.Snapshot, []rulesets.PackStatus, error) {
	direct := subscription.NewHTTPClient(0)
	defer direct.CloseIdleConnections()
	directPacks := rulesets.HTTPFetch(20*time.Second, direct.Transport)
	directGeoIP := rulesets.GeoIPHTTPFetch(20*time.Second, direct.Transport)
	directRelease := rulesets.GeoSiteReleaseHTTPFetch(30*time.Second, direct.Transport)
	if proxyPort <= 0 {
		return rulesets.BuildSelected(ctx, selected, countries, current, directPacks, directRelease, directGeoIP)
	}
	proxy := subscription.NewHTTPClient(proxyPort)
	defer proxy.CloseIdleConnections()
	return rulesets.BuildSelected(ctx, selected, countries, current,
		rulesets.FallbackFetch(rulesets.HTTPFetch(20*time.Second, proxy.Transport), directPacks),
		rulesets.GeoSiteReleaseHTTPFetch(30*time.Second, proxy.Transport),
		rulesets.FallbackFetch(rulesets.GeoIPHTTPFetch(20*time.Second, proxy.Transport), directGeoIP), directRelease)
}

// Retain both confirmed and pending references before changing a live routing table.
func retainGeoIPAssets(paths Paths, bases ...xray.BaseConfig) error {
	docs := make([]json.RawMessage, 0, len(bases))
	for _, base := range bases {
		body, err := xray.RenderBase(base)
		if err != nil {
			return err
		}
		docs = append(docs, body)
	}
	body, err := json.Marshal(map[string]any{"configurations": docs})
	if err != nil {
		return err
	}
	target := filepath.Join(paths.RunRoot, "geoip-retention.json")
	tmp, err := os.CreateTemp(paths.RunRoot, ".geoip-retention-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(body)
	}
	if err == nil {
		err = tmp.Sync()
	}
	err = errors.Join(err, tmp.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, target)
}

// Cleanup also runs after the final GeoIP rule has been removed. The atomic
// retention document protects active, candidate and rollback configurations.
func runGeoIPCleanup(ctx context.Context, paths Paths) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		_, err := geoipasset.Prune(filepath.Join(paths.DataRoot, "geoip-assets"), []string{filepath.Join(paths.RunRoot, "xray.json"), filepath.Join(paths.RunRoot, "geoip-retention.json")}, []string{filepath.Join(paths.DataRoot, "generations")}, nil, time.Now())
		if err != nil {
			log.Printf("resproxy: GeoIP cleanup deferred: %v", err)
		}
	}
}
