package daemon

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/rulesets"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func selectedRulesetSources(config settings.Config) ([]string, map[string]rulesets.CountrySelection) {
	var byPolicy [][]string
	countries := map[string]rulesets.CountrySelection{}
	for _, policy := range config.Base.Policies {
		for _, rule := range policy.Rules {
			byPolicy = append(byPolicy, rule.ServicePacks)
			if rule.Country == "" {
				continue
			}
			if rule.UseGeosite || rule.UseGeoIP {
				choice := countries[rule.Country]
				choice.Geosite = choice.Geosite || rule.UseGeosite
				choice.GeoIP = choice.GeoIP || rule.UseGeoIP
				countries[rule.Country] = choice
			}
		}
	}
	return rulesets.SelectedFromPolicies(byPolicy), countries
}

func initialRulesetProgress(config settings.Config, snapshot rulesets.Snapshot) rulesetStatus {
	selected, countries := selectedRulesetSources(config)
	packs, err := rulesets.PendingSelected(selected, countries, snapshot)
	status := rulesetStatus{Phase: "starting", ActiveUpdated: snapshot.UpdatedAt, Packs: packs}
	if err != nil {
		status.Phase, status.Error = "error", err.Error()
	} else if len(packs) == 0 {
		status.Phase = "unused"
	}
	return status
}

func initialRulesetDelay(status rulesetStatus, countries map[string]rulesets.CountrySelection) time.Duration {
	for _, pack := range status.Packs {
		if pack.State == "pending" {
			return 0 // A newly attached rule must not wait for the periodic timer.
		}
	}
	if len(countries) > 0 {
		return 5 * time.Second
	}
	return 120 * time.Second
}

func publishRulesetProgress(paths Paths, status rulesetStatus) {
	body, err := json.Marshal(status)
	if err == nil {
		var file *os.File
		file, err = os.CreateTemp(paths.RunRoot, ".rulesets-status-")
		if err == nil {
			defer os.Remove(file.Name())
			_, err = file.Write(body)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(file.Name(), filepath.Join(paths.RunRoot, "rulesets-status.json"))
			}
		}
	}
	if err != nil {
		log.Printf("resproxy: ruleset status unavailable: %v", err)
	}
}
