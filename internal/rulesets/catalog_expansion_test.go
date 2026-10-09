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
	if err != nil || !reflect.DeepEqual(matches, []string{"domain:bank.example", "domain:fresh.example"}) {
		t.Fatal(matches, err)
	}
}
