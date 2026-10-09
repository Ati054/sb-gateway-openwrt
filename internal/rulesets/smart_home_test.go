package rulesets

import (
	"slices"
	"testing"
)

func TestSmartHomeCardsHaveIndependentNarrowScopes(t *testing.T) {
	choices, err := Featured()
	if err != nil {
		t.Fatal(err)
	}
	packs, err := index()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ewelink", "xiaomi-home", "tuya", "aqara", "shelly"} {
		card := packs[id]
		if card.Country != "ww" || card.UpstreamName != "" || len(card.IncludedPackIDs)+len(card.IPCIDRs) != 0 || len(card.FallbackDomains) == 0 {
			t.Fatalf("invalid smart-home scope %s: %+v", id, card)
		}
		if !slices.ContainsFunc(choices, func(choice Choice) bool { return choice.ID == id && choice.Country == "ww" }) {
			t.Fatal("card not selectable", id)
		}
		expressions, err := Expressions([]string{id}, Snapshot{})
		if err != nil {
			t.Fatal(err)
		}
		if len(expressions) != len(card.FallbackDomains) {
			t.Fatal("scope unexpectedly expanded", id)
		}
		for _, domain := range card.FallbackDomains {
			if !slices.Contains(expressions, "domain:"+domain) {
				t.Fatal("domain missing", id, domain)
			}
		}
		if id == "xiaomi-home" && slices.Contains(expressions, "domain:mi.com") {
			t.Fatal("Xiaomi scope captures unrelated services")
		}
	}
}
