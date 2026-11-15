package rulesets

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestFailedInitialDownloadNeverCreatesHiddenDomains(t *testing.T) {
	snap, _, err := Build(context.Background(), []string{"cn-ai", "ru-government"}, Snapshot{}, func(context.Context, string) (string, error) { return "", errors.New("offline") })
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Packs) != 0 {
		t.Fatal("hidden data created", snap)
	}
	matches, err := Expressions([]string{"cn-ai", "ru-government"}, snap)
	if err != nil || len(matches) != 0 {
		t.Fatal("offline defaults applied", matches, err)
	}
}
func TestDownloadedCategoriesStayWithinSelectedScope(t *testing.T) {
	snap := Snapshot{Packs: map[string]Rules{"cn-ai": {Suffix: []string{"fresh.example"}}, "cn-banks": {Suffix: []string{"bank.example"}}, "ir-banks": {Suffix: []string{"iran.example"}}}}
	matches, err := Expressions([]string{"cn-ai"}, snap)
	if err != nil || !reflect.DeepEqual(matches, []string{"domain:fresh.example"}) {
		t.Fatal(matches, err)
	}
	matches, err = Expressions([]string{"cn-all"}, snap)
	if err != nil || !reflect.DeepEqual(matches, []string{"domain:bank.example", "domain:cn", "domain:fresh.example", "domain:xn--fiqs8s", "domain:xn--fiqz9s"}) {
		t.Fatal(matches, err)
	}
}

func TestCountryAggregatesIncludeTheirZonesWithoutExpandingIndividualCards(t *testing.T) {
	snapshot := Snapshot{Packs: map[string]Rules{
		"ir-banks": {Suffix: []string{"bank.example"}},
		"cn-banks": {Suffix: []string{"bank-cn.example"}},
	}}
	for _, tc := range []struct {
		card string
		want []string
	}{
		{"ir-all", []string{"domain:bank.example", "domain:ir", "domain:xn--mgba3a4f16a"}},
		{"cn-all", []string{"domain:bank-cn.example", "domain:cn", "domain:xn--fiqs8s", "domain:xn--fiqz9s"}},
		{"ir-banks", []string{"domain:bank.example"}},
		{"cn-banks", []string{"domain:bank-cn.example"}},
	} {
		t.Run(tc.card, func(t *testing.T) {
			got, err := Expressions([]string{tc.card}, snapshot)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("scope mismatch: %v, error %v", got, err)
			}
			component, err := PackExpressions(tc.card, Snapshot{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.card == "ir-banks" || tc.card == "cn-banks" {
				if len(component) != 0 {
					t.Fatalf("individual category gained a fallback: %v", component)
				}
			} else if len(component) < 2 {
				t.Fatalf("country aggregate zones absent from reusable component: %v", component)
			}
		})
	}
}
