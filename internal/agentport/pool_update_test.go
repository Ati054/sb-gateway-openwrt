package agentport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"example.org/resilient-proxy/internal/catalog"
	"example.org/resilient-proxy/internal/health"
	"example.org/resilient-proxy/internal/settings"
)

func TestPoolHealthEditKeepsPublishedGenerationAndRejectsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	policies := []settings.Policy{{ID: "all", Mode: "best", SelectionOrder: []string{"all:*"}, Health: health.Policy{ActiveLivenessSeconds: 3}}}
	node := catalog.Node{ID: "fresh", ProviderID: "fixture", Country: "CA", Label: "latest", Protocol: "vless", Server: "127.0.0.1", Port: 443, Transport: "tcp", Credentials: map[string]string{"uuid": "11111111-1111-4111-8111-111111111111"}}
	if err := WritePool(path, []catalog.Node{node}, policies); err != nil {
		t.Fatal(err)
	}
	policies[0].Health.ActiveLivenessSeconds = 9
	if err := UpdatePoolHealth(context.Background(), path, policies); err != nil {
		t.Fatal(err)
	}
	var pool healthPool
	if err := readJSON(path, &pool); err != nil {
		t.Fatal(err)
	}
	if pool.Outbounds["fresh"] == nil || pool.HealthPolicies["resproxy-all"].Policy.ActiveLivenessSeconds != 9 {
		t.Fatal("health edit replaced inventory")
	}
	before, _ := os.ReadFile(path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	policies[0].Health.ActiveLivenessSeconds = 10
	if err := UpdatePoolHealth(ctx, path, policies); err == nil {
		t.Fatal("cancelled health edit accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("cancelled health edit changed pool")
	}
}
