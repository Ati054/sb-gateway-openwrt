package daemon

import (
	"errors"
	"os"
	"testing"

	"github.com/Ati054/xroute-openwrt/internal/catalog"
	"github.com/Ati054/xroute-openwrt/internal/generation"
	"github.com/Ati054/xroute-openwrt/internal/settings"
)

func TestRetainConfiguredSourcesRemovesDeletedProvider(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	nodes := []catalog.Node{
		{ID: "one", ProviderID: "primary", Server: "one.test", Port: 443},
		{ID: "two", ProviderID: "reserve", Server: "two.test", Port: 443},
	}
	id, err := store.Stage(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(id); err != nil {
		t.Fatal(err)
	}
	retained, err := retainConfiguredSources(store, nodes, []settings.Subscription{{ID: "primary"}})
	if err != nil || len(retained) != 1 || retained[0].ProviderID != "primary" {
		t.Fatalf("retained = %+v, %v", retained, err)
	}
	_, published, err := store.Current()
	if err != nil || len(published) != 1 || published[0].ProviderID != "primary" {
		t.Fatalf("published = %+v, %v", published, err)
	}
	retained, err = retainConfiguredSources(store, published, nil)
	if err != nil || len(retained) != 0 {
		t.Fatalf("last source retained: %+v %v", retained, err)
	}
	if _, _, err := store.Current(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty configuration retained a current catalog: %v", err)
	}
}

func TestStartupSourceFilteringDoesNotCommitBeforeReadiness(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	nodes := []catalog.Node{{ID: "old", ProviderID: "old-source", Server: "old.test", Port: 443}}
	id, err := store.Stage(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(id); err != nil {
		t.Fatal(err)
	}
	if filtered := configuredSourceNodes(nodes, []settings.Subscription{{ID: "new-source"}}); len(filtered) != 0 {
		t.Fatal(filtered)
	}
	current, durable, err := store.Current()
	if err != nil || current != id || len(durable) != 1 || durable[0].ProviderID != "old-source" {
		t.Fatal("a failed new startup cannot recover the previous catalog", current, durable, err)
	}
}
