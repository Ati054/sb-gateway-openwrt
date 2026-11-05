package generation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

func indexedTestNode(id string) catalog.Node {
	return catalog.Node{ID: id, ProviderID: "source", Protocol: "vless", Server: id + ".example", Port: 443,
		Settings: map[string]any{"tls": map[string]any{"enabled": true}}, Credentials: map[string]string{"uuid": "private-test-credential"}}
}

func TestIndexedPublicationKeepsLegacyAndResolvesOneRecord(t *testing.T) {
	s := Store{Root: t.TempDir()}
	defer catalog.ReleaseRecordReaders(s.IndexedRoot())
	old, err := s.Stage([]catalog.Node{indexedTestNode("old")})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Publish(old); err != nil {
		t.Fatal(err)
	}
	id, err := s.StageIndexed(t.Context(), []catalog.Node{indexedTestNode("new")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := s.CurrentMetadata()
	if err != nil || current != old {
		t.Fatal("staging replaced the published legacy catalogue")
	}
	if err = s.Publish(id); err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(filepath.Join(s.Root, "CURRENT"))
	if err != nil || strings.TrimSpace(string(legacy)) != old {
		t.Fatal("the previous package lost its readable rollback pointer")
	}
	_, nodes, err := s.CurrentMetadata()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("metadata: %v", err)
	}
	if nodes[0].Settings != nil || nodes[0].Credentials != nil || nodes[0].Record == nil {
		t.Fatal("full records entered the metadata view")
	}
	body, _ := json.Marshal(nodes)
	if strings.Contains(string(body), "private-test-credential") {
		t.Fatal("credentials entered public metadata")
	}
	full, err := nodes[0].Materialize(t.Context())
	if err != nil || full.Credentials["uuid"] != "private-test-credential" || full.ID != "new" {
		t.Fatalf("one-record lookup failed: %v", err)
	}
	if _, err = s.Load(old); err != nil {
		t.Fatal("legacy rollback catalogue was lost", err)
	}
}

func TestIndexedPruningRespectsLiveRuntimeLease(t *testing.T) {
	s := Store{Root: t.TempDir()}
	defer catalog.ReleaseRecordReaders(s.IndexedRoot())
	first, err := s.StageIndexed(t.Context(), []catalog.Node{indexedTestNode("first")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := s.LoadMetadata(first)
	if err != nil {
		t.Fatal(err)
	}
	release, err := catalog.PinRecordRoot(t.Context(), nodes[0].Record.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, name := range []string{"second", "third"} {
		id, err := s.StageIndexed(t.Context(), []catalog.Node{indexedTestNode(name)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Publish(id); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Prune(2); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(s.IndexedRoot(), first)); err != nil {
		t.Fatal("active pool lost its record generation", err)
	}
	release()
	if err = s.Prune(2); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(s.IndexedRoot(), first)); !os.IsNotExist(err) {
		t.Fatal("retired generation was not pruned", err)
	}
}

func TestIndexedFailedRecordNeverReplacesCurrent(t *testing.T) {
	s := Store{Root: t.TempDir()}
	defer catalog.ReleaseRecordReaders(s.IndexedRoot())
	id, err := s.StageIndexed(context.Background(), []catalog.Node{indexedTestNode("good")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Publish(id); err != nil {
		t.Fatal(err)
	}
	bad := indexedTestNode("oversized")
	bad.Settings = map[string]any{"untrusted": strings.Repeat("x", 2<<20)}
	if _, err = s.StageIndexed(t.Context(), []catalog.Node{bad}, nil); err == nil {
		t.Fatal("record byte limit was ignored")
	}
	actual, _, err := s.CurrentMetadata()
	if err != nil || actual != id {
		t.Fatal("failed stage changed CURRENT")
	}
}
