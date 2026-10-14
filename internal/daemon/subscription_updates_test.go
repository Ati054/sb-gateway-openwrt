package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func TestSubscriptionResultsFollowPublishedSource(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir(), DataRoot: t.TempDir()}
	sources := []settings.Subscription{{ID: "one"}, {ID: "two"}}
	u := newSubscriptionUpdates(paths, sources)
	before := []catalog.Node{{ID: "a", ProviderID: "one"}, {ID: "foreign", ProviderID: "two"}}
	after := []catalog.Node{{ID: "b", ProviderID: "one"}, {ID: "c", ProviderID: "one"}, {ID: "foreign", ProviderID: "two"}}
	attempt := u.begin("one", before)
	if s := SubscriptionUpdates(paths, before)["one"]; s.State != "loading" || s.Nodes != 1 || s.Attempt != attempt {
		t.Fatal(s)
	}
	u.finish("one", attempt, before, after, nil)
	success := SubscriptionUpdates(paths, after)["one"]
	if success.State != "success" || success.Nodes != 2 || success.Added != 2 || success.Removed != 1 || success.UpdatedAt.IsZero() {
		t.Fatal(success)
	}
	attempt = u.begin("one", after)
	u.finish("one", attempt, after, after, errors.New("no reachable node in batch"))
	failed := SubscriptionUpdates(paths, after)["one"]
	if failed.State != "error" || failed.Error != "unreachable" || failed.Nodes != 2 || !failed.UpdatedAt.Equal(success.UpdatedAt) {
		t.Fatal(failed)
	}
	if SubscriptionUpdates(paths, after)["two"].Nodes != 1 {
		t.Fatal("foreign source count changed")
	}
	if entries, _ := os.ReadDir(paths.DataRoot); len(entries) != 0 {
		t.Fatal("status wrote to persistent storage")
	}
}

func TestSubscriptionStatusRestartAndDeletion(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	u := newSubscriptionUpdates(paths, []settings.Subscription{{ID: "one"}, {ID: "removed"}})
	u.begin("one", nil)
	restored := newSubscriptionUpdates(paths, []settings.Subscription{{ID: "one"}})
	if restored.values["one"].Error != "interrupted" || len(restored.values) != 1 {
		t.Fatal(restored.values)
	}
	body, _ := os.ReadFile(filepath.Join(paths.RunRoot, "subscription-status.json"))
	if len(body) > 1024 {
		t.Fatal("unbounded result record")
	}
	// Catalog counts remain available even when volatile metadata is lost on reboot.
	os.Remove(filepath.Join(paths.RunRoot, "subscription-status.json"))
	if SubscriptionUpdates(paths, []catalog.Node{{ID: "a", ProviderID: "one"}})["one"].Nodes != 1 {
		t.Fatal("lost catalog count")
	}
}
