package generation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

func TestStageDoesNotPublishAndSecretsAreSeparate(t *testing.T) {
	store := Store{Root: t.TempDir()}
	nodes := []catalog.Node{{ID: "node-one", ProviderID: "provider-a", Protocol: "vless", Server: "edge.example", Port: 443,
		Credentials: map[string]string{"uuid": "private-uuid"}}}
	id, err := store.Stage(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Current(); !os.IsNotExist(err) {
		t.Fatalf("staging became current: %v", err)
	}
	public, err := os.ReadFile(filepath.Join(store.Root, "generations", id, "catalog.json"))
	if err != nil || strings.Contains(string(public), "private-uuid") {
		t.Fatalf("public catalog leaked credentials: %v", err)
	}
	if err := store.Publish(id); err != nil {
		t.Fatal(err)
	}
	actualID, current, err := store.Current()
	if err != nil || actualID != id || len(current) != 1 || current[0].Credentials["uuid"] != "private-uuid" {
		t.Fatalf("published generation was not restored: %s %+v %v", actualID, current, err)
	}
}

func TestFailedCandidateLeavesCurrentAndIntegrityIsChecked(t *testing.T) {
	store := Store{Root: t.TempDir()}
	first := []catalog.Node{{ID: "one", ProviderID: "provider-a", Protocol: "vless", Server: "first.example", Port: 443}}
	id, err := store.Stage(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(id); err != nil {
		t.Fatal(err)
	}
	second := []catalog.Node{{ID: "two", ProviderID: "provider-a", Protocol: "vless", Server: "second.example", Port: 443}}
	staged, err := store.Stage(second)
	if err != nil {
		t.Fatal(err)
	}
	if current, _, err := store.Current(); err != nil || current != id {
		t.Fatalf("unpublished candidate displaced current generation: %s %v", current, err)
	}
	secretPath := filepath.Join(store.Root, "generations", staged, "secrets.json")
	if err := os.WriteFile(secretPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(staged); err == nil {
		t.Fatal("corrupt staged generation was published")
	}
	if current, _, err := store.Current(); err != nil || current != id {
		t.Fatalf("failed publish displaced previous generation: %s %v", current, err)
	}
}

func TestInvalidGenerationIDCannotEscapeStore(t *testing.T) {
	store := Store{Root: t.TempDir()}
	for _, id := range []string{"../outside", "", strings.Repeat("g", 64)} {
		if _, err := store.Load(id); err == nil {
			t.Fatalf("accepted invalid generation ID %q", id)
		}
	}
}

func TestPruneKeepsCurrentAndTwoRecentGenerations(t *testing.T) {
	store := Store{Root: t.TempDir()}
	ids := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		id, err := store.Stage([]catalog.Node{{ID: "node", ProviderID: "provider-a", Protocol: "vless",
			Server: string(rune('a'+index)) + ".example", Port: 443}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		when := time.Now().Add(time.Duration(index-10) * time.Hour)
		if err := os.Chtimes(filepath.Join(store.Root, "generations", id), when, when); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Publish(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(3); err != nil {
		t.Fatal(err)
	}
	for index, id := range ids {
		_, err := os.Stat(filepath.Join(store.Root, "generations", id))
		wantRetained := index == 0 || index >= 3
		if wantRetained != (err == nil) {
			t.Fatalf("generation %d retention=%t, stat=%v", index, wantRetained, err)
		}
	}
	if current, _, err := store.Current(); err != nil || current != ids[0] {
		t.Fatalf("prune damaged current generation: %s %v", current, err)
	}
}

func TestPruneOnlyRemovesOldStagingDirectories(t *testing.T) {
	store := Store{Root: t.TempDir()}
	base := filepath.Join(store.Root, "generations")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(base, ".staging-old")
	fresh := filepath.Join(base, ".staging-fresh")
	unrelated := filepath.Join(base, "user-notes")
	for _, path := range []string{old, fresh, unrelated} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	then := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, then, then); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old staging directory survived: %v", err)
	}
	for _, path := range []string{fresh, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unrelated directory was removed: %s %v", path, err)
		}
	}
}

func TestUnchangedPublicationDoesNotRewriteFlashPointer(t *testing.T) {
	store := Store{Root: t.TempDir()}
	nodes := []catalog.Node{{ID: "a", SelectionIdentity: "stable", ProviderID: "test", Label: "Test", Protocol: "vless", Server: "example.test", Port: 443, Credentials: map[string]string{"uuid": "00000000-0000-4000-8000-000000000001"}}}
	id, err := store.Stage(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(id); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, "CURRENT")
	before := time.Unix(1234, 0)
	if err := os.Chtimes(path, before, before); err != nil {
		t.Fatal(err)
	}
	if err := store.Publish(id); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !after.ModTime().Equal(before) {
		t.Fatal("unchanged catalog rewrote pointer", err)
	}
}
