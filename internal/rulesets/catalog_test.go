package rulesets

import "testing"

func TestCountryCatalogAndSelections(t *testing.T) {
	choices, err := Featured()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, choice := range choices {
		counts[choice.Country]++
		if choice.Name == "" {
			t.Fatal("unnamed card", choice.ID)
		}
		if err := ValidateSelection([]string{choice.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if counts["ru"] != 10 || counts["cn"] != 30 || counts["ir"] != 16 {
		t.Fatal(counts)
	}
	if ValidateSelection([]string{"not-a-pack"}) == nil {
		t.Fatal("unknown card accepted")
	}
}
