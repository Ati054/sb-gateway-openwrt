package rulesets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuildPreservesLastGoodDownloadedCopy(t *testing.T) {
	previous := Snapshot{Version: SnapshotVersion, Packs: map[string]Rules{
		"ru-banks": {Suffix: []string{"last-good.example"}},
	}}
	fetch := func(_ context.Context, name string) (string, error) {
		if name == "category-gov-ru" {
			return "domain:current.example", nil
		}
		return "", errors.New("offline")
	}
	got, statuses, err := Build(context.Background(), []string{"ru-banks", "ru-government"}, previous, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Packs["ru-banks"].Suffix, []string{"last-good.example"}) ||
		!reflect.DeepEqual(got.Packs["ru-government"].Suffix, []string{"current.example"}) {
		t.Fatalf("last good or updated pack lost: %+v", got.Packs)
	}
	if statuses[0].State != "stale" || statuses[1].State != "updated" {
		t.Fatalf("unexpected statuses: %+v", statuses)
	}
	values, err := Expressions([]string{"ru-banks", "ru-government"}, got)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"domain:last-good.example", "domain:current.example"} {
		if !containsString(values, expected) {
			t.Errorf("missing %q", expected)
		}
	}
}

func TestCountryPacksFetchOnlySelectedData(t *testing.T) {
	var fetched []string
	fetch := func(_ context.Context, name string) (string, error) {
		fetched = append(fetched, name)
		return "domain:live.example", nil
	}
	got, _, err := Build(context.Background(), []string{"cn-banks"}, Snapshot{}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fetched, []string{"category-bank-cn"}) || len(got.Packs) != 1 {
		t.Fatalf("unexpected country fetches: %v, packs=%v", fetched, got.Packs)
	}
	if !containsString(got.Packs["cn-banks"].Suffix, "live.example") {
		t.Fatal("selected country pack was not published")
	}
}

func TestSnapshotAtomicRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rulesets.json")
	value := Snapshot{Version: SnapshotVersion, Packs: map[string]Rules{"ru-all": {Suffix: []string{"example.ru"}}}}
	if err := Save(path, value); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got.Packs, value.Packs) {
		t.Fatalf("snapshot round-trip: %+v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"packs":{"unknown":{"suffix":["bad"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("accepted unknown persisted pack")
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"packs":{"ru-all":{"regex":["["]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("accepted invalid persisted regex")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
