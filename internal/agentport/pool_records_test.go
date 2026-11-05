package agentport

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

func TestIndexedHealthPoolUsesMetadataAndLazyDefinitions(t *testing.T) {
	s := generation.Store{Root: t.TempDir()}
	defer catalog.ReleaseRecordReaders(s.IndexedRoot())
	nodes := []catalog.Node{{ID: "a", Label: "First", ProviderID: "source", Protocol: "vless", Server: "first.example", Port: 443, Credentials: map[string]string{"uuid": "2f1c08fc-3f43-4e95-8f57-f8bded750a1a"}},
		{ID: "b", Label: "Second", ProviderID: "source", Protocol: "vless", Server: "second.example", Port: 443, Credentials: map[string]string{"uuid": "96f81313-e2c0-4a16-bbb6-9e54d393761b"}}}
	id, err := s.StageIndexed(t.Context(), nodes, xray.RenderOutbound)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := s.LoadMetadata(id)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PreparePool(metadata, []settings.Policy{{ID: "all", Mode: "best", SelectionOrder: []string{"all:*"}}})
	if err != nil {
		t.Fatal(err)
	}
	pool := prepared.pool
	inline, err := PreparePool(nodes, []settings.Policy{{ID: "all", Mode: "best", SelectionOrder: []string{"all:*"}}})
	if err != nil {
		t.Fatal(err)
	}
	legacyRuntime, indexedRuntime := newXraySelectorRuntime(Options{}), newXraySelectorRuntime(Options{})
	legacyRuntime.pool, indexedRuntime.pool = inline.pool, pool
	for _, node := range nodes {
		if legacyRuntime.dynamicTag("route-", node.ID) != indexedRuntime.dynamicTag("route-", node.ID) {
			t.Fatal("catalogue format changed the selected node's runtime tag")
		}
	}
	if pool.Version != 5 || len(pool.Outbounds) != 0 || len(pool.CatalogKeys) != 2 || pool.CatalogRef == nil {
		t.Fatal("health pool expanded private definitions")
	}
	body, _ := json.Marshal(pool)
	if bytes.Contains(body, []byte(nodes[0].Credentials["uuid"])) {
		t.Fatal("credential embedded in health pool")
	}
	pool.ProbeLanes, pool.ProbeBudget = 0, 8
	if healthProbeLaneCount(pool) != 2 {
		t.Fatal("listener geometry used inline count instead of indexed metadata")
	}
	definition, err := pool.outboundDefinition(t.Context(), "b")
	if err != nil || !bytes.Contains(definition, []byte(nodes[1].Credentials["uuid"])) {
		t.Fatal("lazy definition lookup failed", err)
	}
	pool.CatalogKeys["b"] = "wrong"
	if _, err = pool.outboundDefinition(t.Context(), "b"); err == nil {
		t.Fatal("definition fingerprint mismatch was accepted")
	}
}
