package refresh

import (
	"testing"

	"github.com/Ati054/xroute-openwrt/internal/catalog"
	"github.com/Ati054/xroute-openwrt/internal/generation"
)

func TestRefreshCannotReintroduceRemovedProviderFromDurableCatalog(t *testing.T) {
	store := generation.Store{Root: t.TempDir(), MetadataRoot: t.TempDir()}
	seed := Service{Store: store}
	for provider, feed := range map[string]string{"removed": linkCA, "retained": linkUS} {
		if _, err := seed.Import(t.Context(), provider, []byte(feed), 0); err != nil {
			t.Fatal(err)
		}
	}
	_, old, err := store.CurrentMetadata()
	if err != nil || len(old) != 2 {
		t.Fatal(old, err)
	}
	// Model a failed startup cleanup: durable CURRENT still contains both.
	check := func(nodes []catalog.Node) {
		t.Helper()
		if len(nodes) != 1 || nodes[0].ProviderID != "retained" {
			t.Fatal("retired source escaped into runtime", nodes)
		}
	}
	service := Service{Store: store, Runtime: &fakeRuntime{}, AllowedProviders: map[string]bool{"retained": true}, AfterSwitch: func(previous, next []catalog.Node) (func() error, error) {
		check(previous)
		check(next)
		return func() error { return nil }, nil
	}}
	if _, err := service.Apply(t.Context(), "retained", []byte(linkUS), 0, "main", func(nodes []catalog.Node) (string, error) { check(nodes); return nodes[0].ID, nil }); err != nil {
		t.Fatal(err)
	}
	_, now, err := store.CurrentMetadata()
	if err != nil {
		t.Fatal(err)
	}
	check(now)
	if _, err := service.Apply(t.Context(), "removed", []byte(linkCA), 0, "main", firstNode); err == nil {
		t.Fatal("removed source admitted a late refresh")
	}
	service.AllowedProviders = map[string]bool{}
	if _, err := service.Apply(t.Context(), "retained", []byte(linkUS), 0, "main", firstNode); err == nil {
		t.Fatal("empty configured provider set treated as unrestricted")
	}
}
