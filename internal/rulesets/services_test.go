package rulesets

import (
	"slices"
	"testing"
)

func TestPopularServiceCardsIncludeApplicationEndpoints(t *testing.T) {
	for _, row := range []struct{ id, domain string }{{"youtube", "googlevideo.com"}, {"discord", "discordapp.net"}, {"openai", "oaiusercontent.com"}, {"okx", "wsus.okx.com"}} {
		got, err := Expressions([]string{row.id}, Snapshot{})
		if err != nil || !slices.Contains(got, "domain:"+row.domain) {
			t.Fatal("service dependency missing", row, got, err)
		}
	}
	networks, err := ServiceCIDRs([]string{"telegram"})
	if err != nil || !slices.Contains(networks, "149.154.160.0/20") || !slices.Contains(networks, "2001:b28:f23d::/48") {
		t.Fatal("Telegram destinations missing", networks, err)
	}
	choices, err := Featured()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range choices {
		if c.Country == "ww" {
			seen[c.ID] = true
		}
	}
	for _, id := range []string{"youtube", "discord", "telegram", "whatsapp", "instagram", "facebook", "tiktok", "netflix", "spotify", "openai", "claude", "antigravity", "github", "steam", "okx"} {
		if !seen[id] {
			t.Fatal("service card missing", id)
		}
	}
}

func TestExchangeCardsIncludeAPIsAndAcceptLegacyAPIRules(t *testing.T) {
	choices, err := Featured()
	if err != nil {
		t.Fatal(err)
	}
	for _, api := range []string{"okx-api", "bybit-api", "gateio-api"} {
		for _, choice := range choices {
			if choice.ID == api {
				t.Fatal("API shown separately", api)
			}
		}
		legacy, err := Expressions([]string{api}, Snapshot{})
		if err != nil || len(legacy) == 0 {
			t.Fatal(api, legacy, err)
		}
		parent := api[:len(api)-4]
		combined, err := Expressions([]string{parent}, Snapshot{})
		if err != nil {
			t.Fatal(err)
		}
		for _, domain := range legacy {
			if !slices.Contains(combined, domain) {
				t.Fatal(parent, "missing API endpoint", domain)
			}
		}
	}
}
