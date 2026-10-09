// Package rulesets exposes reviewed service packs and selected country lists.
// Updates retain the last good snapshot and publish routing changes live.
package rulesets

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

//go:embed service-packs.json
var catalogJSON []byte

type pack struct {
	ID              string   `json:"id"`
	Country         string   `json:"country"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	IncludedPackIDs []string `json:"included_pack_ids"`
	IPCIDRs         []string `json:"ip_cidrs,omitempty"`
	FallbackDomains []string `json:"fallback_domains,omitempty"`
	UpstreamName    string   `json:"upstream_name"`
}

type Choice struct {
	ID          string `json:"id"`
	Country     string `json:"country"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

var featuredIDs = []string{
	"youtube", "discord", "telegram", "whatsapp", "instagram", "facebook", "tiktok", "netflix", "spotify", "openai", "claude", "antigravity", "github", "steam", "okx",
	"binance", "bybit", "gateio",
	"ewelink", "xiaomi-home", "tuya", "aqara", "shelly",
	"ru-government", "ru-banks", "ru-marketplaces", "yandex", "mailru-group",
	"ru-travel", "ru-telecom", "ru-media", "ru-education-work-it", "ru-all",
	"ir-ads", "cn-ai", "cn-automobile", "cn-banks", "ir-banks", "cn-blog", "ir-bourse", "cn-cdn", "cn-collaborate", "cn-dev", "cn-documents", "cn-education", "ir-education", "cn-electronic", "cn-enterprise-query-platform", "cn-entertainment", "cn-food", "ir-forums", "cn-game-accelerator", "cn-games", "ir-government", "cn-hospital", "cn-httpdns", "ir-insurance", "cn-logistics", "cn-media", "ir-media", "cn-mooc", "cn-netdisk", "cn-network-security", "ir-news", "cn-ntp", "cn-number-verification", "cn-outsource", "ir-payment", "cn-scholar", "ir-scholar", "cn-securities", "ir-shopping", "cn-social", "ir-social-media", "ir-tech", "ir-travel", "cn-wiki", "cn-all", "ir-all",
}

func index() (map[string]pack, error) {
	var catalog []pack
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		return nil, fmt.Errorf("decode service-pack baseline: %w", err)
	}
	result := make(map[string]pack, len(catalog))
	for _, item := range catalog {
		if item.ID == "" || result[item.ID].ID != "" {
			return nil, errors.New("invalid or duplicate service-pack ID")
		}
		result[item.ID] = item
	}
	for _, item := range catalog {
		for _, included := range item.IncludedPackIDs {
			if result[included].ID == "" {
				return nil, fmt.Errorf("unknown included service pack %q", included)
			}
		}
	}
	return result, nil
}

func Featured() ([]Choice, error) {
	packs, err := index()
	if err != nil {
		return nil, err
	}
	choices := make([]Choice, 0, len(featuredIDs))
	for _, id := range featuredIDs {
		item := packs[id]
		if item.ID == "" {
			return nil, fmt.Errorf("featured service pack %q is missing", id)
		}
		country := item.Country
		if country == "" {
			country = "ru"
		}
		choices = append(choices, Choice{ID: id, Country: country, Name: item.Name,
			Description: item.Description})
	}
	return choices, nil
}

// ValidateSelection checks selectable cards and their dependency graph.
func ValidateSelection(selected []string) error {
	packs, err := index()
	if err != nil {
		return err
	}
	_, err = selectedPacks(selected, packs)
	return err
}
