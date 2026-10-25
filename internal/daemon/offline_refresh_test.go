package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
	"github.com/Ati054/sb-gateway-openwrt/internal/selection"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func TestOfflineImportPreparesCountriesWithoutStartingService(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir(), DataRoot: t.TempDir(), Binary: filepath.Join(t.TempDir(), "must-not-start")}
	config := settings.Config{Subscriptions: []settings.Subscription{
		{ID: "first", URL: "https://first.example.test/feed", MaxNodes: 10},
		{ID: "other", URL: "https://other.example.test/feed", MaxNodes: 10},
	}}
	feeds := map[string]string{
		"first": "vless://2f1c08fc-3f43-4e95-8f57-f8bded750a1a@ca.example.test:443?security=tls#CA%20Toronto",
		"other": "vless://96f81313-e2c0-4a16-bbb6-9e54d393761b@us.example.test:443?security=tls#US%20Chicago",
	}
	updates := newSubscriptionUpdates(paths, config.Subscriptions)
	fetch := func(_ context.Context, source settings.Subscription) ([]byte, error) {
		return []byte(feeds[source.ID]), nil
	}
	verify := func(context.Context, settings.Subscription) error { return nil }
	for _, id := range []string{"first", "other"} {
		if err := importOffline(context.Background(), config, paths, id, updates, fetch, verify); err != nil {
			t.Fatal(err)
		}
	}
	store := generation.Store{Root: paths.DataRoot, MetadataRoot: paths.RunRoot}
	id, nodes, err := store.Current()
	if err != nil || len(nodes) != 2 {
		t.Fatalf("offline catalog: %d nodes, %v", len(nodes), err)
	}
	inventory := selection.PublicInventory(nodes)
	if len(inventory.Nodes) != 2 {
		t.Fatalf("prepared inventory missing nodes: %+v", inventory)
	}
	for _, source := range config.Subscriptions {
		if state := SubscriptionUpdates(paths, nodes)[source.ID]; state.State != "success" || state.Nodes != 1 {
			t.Fatalf("incorrect offline status: %+v", state)
		}
	}
	for _, path := range []string{"xray.json", "daemon.pid", "reload.sock", "xray-ready", "dnsmasq-handoff.json", "dns-handoff.json"} {
		for _, root := range []string{paths.RunRoot, paths.DataRoot} {
			if _, err := os.Stat(filepath.Join(root, path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("offline import created runtime/network state %s: %v", path, err)
			}
		}
	}
	// A bad subscription or a changed source must preserve both providers.
	for _, changed := range []bool{false, true} {
		if changed {
			feeds["first"] = "vless://2f1c08fc-3f43-4e95-8f57-f8bded750a1a@ca.example.test:443?security=tls#CA%20Toronto"
			verify = func(context.Context, settings.Subscription) error { return errors.New("settings changed") }
		} else {
			feeds["first"] = "invalid subscription"
		}
		if err := importOffline(context.Background(), config, paths, "first", updates, fetch, verify); err == nil {
			t.Fatal("invalid/changed subscription was accepted")
		}
		current, nodes, err := store.Current()
		if err != nil || current != id || len(nodes) != 2 || updates.values["first"].State != "error" {
			t.Fatalf("failed import replaced good catalog: %s %d %v", current, len(nodes), err)
		}
	}
}

func TestOfflineImportRejectsProxyOnlyAndEnabledService(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir(), DataRoot: t.TempDir()}
	config := settings.Config{Subscriptions: []settings.Subscription{{ID: "first", MaxNodes: 10, FetchVia: "proxy-only"}}}
	updates := newSubscriptionUpdates(paths, config.Subscriptions)
	for _, enabled := range []bool{false, true} {
		config.Enabled = enabled
		err := importOffline(context.Background(), config, paths, "first", updates,
			func(context.Context, settings.Subscription) ([]byte, error) {
				t.Fatal("forbidden fetch")
				return nil, nil
			},
			func(context.Context, settings.Subscription) error { t.Fatal("unexpected validation"); return nil })
		if err == nil {
			t.Fatal("enabled/proxy-only import allowed")
		}
	}
}

func TestCatalogLockPreventsConcurrentDaemonAndOfflineImport(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	lock, err := acquireCatalog(paths)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := acquireCatalog(paths); err == nil {
		duplicate.Close()
		t.Fatal("concurrent catalog writer allowed")
	}
	lock.Close()
	lock, err = acquireCatalog(paths)
	if err != nil {
		t.Fatal("closed owner retained catalog lock:", err)
	}
	lock.Close()
}
