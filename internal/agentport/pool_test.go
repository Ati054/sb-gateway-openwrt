package agentport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"example.org/resilient-proxy/internal/catalog"
	"example.org/resilient-proxy/internal/health"
	"example.org/resilient-proxy/internal/selection"
	"example.org/resilient-proxy/internal/settings"
	"example.org/resilient-proxy/internal/xray"
)

func TestWritePoolCarriesFullSelectedInventory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health-pool.json")
	nodes := []catalog.Node{
		{ID: "ca", ProviderID: "provider", Protocol: "vless", Server: "ca.example", Port: 443, Country: "CA", Credentials: map[string]string{"uuid": "secret-ca"}},
		{ID: "us", ProviderID: "provider", Protocol: "vless", Server: "us.example", Port: 443, Country: "US", Credentials: map[string]string{"uuid": "secret-us"}},
	}
	policies := []settings.Policy{{ID: "home", Mode: "priority", SelectionOrder: []string{"country:CA", "country:US"}}}
	if err := WritePool(path, nodes, policies); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pool healthPool
	if err := json.Unmarshal(body, &pool); err != nil {
		t.Fatal(err)
	}
	contract := pool.HealthPolicies["resproxy-home"]
	if pool.Version != 4 || len(contract.Candidates) != 2 || len(contract.Groups) != 2 || len(pool.Outbounds) != 2 {
		t.Fatalf("incomplete health pool: %+v", pool)
	}
	if pool.PolicyPrefixes["resproxy-home"] != "resproxy-home-" {
		t.Fatal("dynamic tag prefix missing")
	}
	before := contract.Nodes["ca"].Fingerprint
	if before == "" {
		t.Fatal("outbound fingerprint missing")
	}
	nodes[0].Port = 8443
	if err := WritePool(path, nodes, policies); err != nil {
		t.Fatal(err)
	}
	updatedBody, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var updated healthPool
	if err := json.Unmarshal(updatedBody, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.HealthPolicies["resproxy-home"].Nodes["ca"].Fingerprint == before {
		t.Fatal("endpoint change reused the old health fingerprint")
	}
	runtime := newXraySelectorRuntime(Options{})
	runtime.pool = pool
	if got, want := xray.DynamicTag("resproxy-home", "ca", pool.Outbounds["ca"]), runtime.policyRuntimeTag("resproxy-home", "ca"); got != want {
		t.Fatalf("refresh and health controller disagree on handler tag: %q != %q", got, want)
	}
}

func TestWritePoolPrefiltersOnlyNumericTCPVLESS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health-pool.json")
	nodes := []catalog.Node{
		{ID: "ip", ProviderID: "provider", Protocol: "vless", Server: "127.0.0.1", Port: 443, Country: "CA", Credentials: map[string]string{"uuid": "secret-ca"}},
		{ID: "domain", ProviderID: "provider", Protocol: "vless", Server: "ca.example", Port: 443, Country: "CA", Credentials: map[string]string{"uuid": "secret-ca"}},
	}
	if err := WritePool(path, nodes, []settings.Policy{{ID: "main", Mode: "best", SelectionOrder: []string{"country:CA"}}}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pool healthPool
	if err := json.Unmarshal(body, &pool); err != nil {
		t.Fatal(err)
	}
	if pool.DialTargets["ip"].Address != "127.0.0.1" || pool.DialTargets["ip"].Port != 443 {
		t.Fatalf("numeric VLESS endpoint omitted: %+v", pool.DialTargets)
	}
	if _, included := pool.DialTargets["domain"]; included {
		t.Fatal("domain endpoint must retain the full Xray probe path")
	}
}

func TestWritePoolUsesLiveFilterMembership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	nodes := []catalog.Node{
		{ID: "chosen", ProviderID: "provider", Protocol: "vless", Server: "ca.example", Port: 443, Country: "CA", Label: "premium", Credentials: map[string]string{"uuid": "secret"}},
		{ID: "excluded", ProviderID: "provider", Protocol: "vless", Server: "ca.example", Port: 443, Country: "CA", Label: "premium trial", Credentials: map[string]string{"uuid": "secret"}},
	}
	policy := settings.Policy{ID: "main", Mode: "priority", SelectionOrder: []string{"all:*"}, NodeFilter: selection.Filter{Include: "premium", Exclude: "trial"}}
	if err := WritePool(path, nodes, []settings.Policy{policy}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pool healthPool
	if err = json.Unmarshal(body, &pool); err != nil {
		t.Fatal(err)
	}
	if got := pool.HealthPolicies["resproxy-main"].Candidates; len(got) != 1 || got[0] != "chosen" {
		t.Fatalf("wrong health inventory: %v", got)
	}
}

func TestWritePoolSharedLatencyBudget(t *testing.T) {
	for _, batch := range []int{0, 1, 10, 64} {
		path := filepath.Join(t.TempDir(), "pool.json")
		policies := []settings.Policy{{ID: "main", Mode: "priority", Health: health.Policy{ProbeBatchSize: batch}}}
		if err := WritePool(path, nil, policies); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var pool healthPool
		if err := json.Unmarshal(data, &pool); err != nil {
			t.Fatal(err)
		}
		want := batch
		if want == 0 {
			want = 10
		}
		if pool.ProbeBudget != want || pool.ProbeLanes != want {
			t.Fatal("shared budget or explicit disabled drop lost")
		}
	}
}

// A shared CDN listener is not a shared proxy configuration: credentials,
// TLS SNI and transport paths must retain separate HTTPS evidence.
func TestSharedAddressKeepsDistinctProxyHTTPSResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	nodes := []catalog.Node{}
	for i, variant := range []string{"working", "other-uuid", "other-sni", "other-path"} {
		uuid, sni, wsPath := "fixture-uuid", "a.example", "/a"
		if i == 1 {
			uuid = "another-fixture-uuid"
		}
		if i == 2 {
			sni = "b.example"
		}
		if i == 3 {
			wsPath = "/b"
		}
		nodes = append(nodes, catalog.Node{ID: variant, Protocol: "vless", Server: "192.0.2.1", Port: 443,
			Credentials: map[string]string{"uuid": uuid},
			Settings: map[string]any{
				"tls":       map[string]any{"enabled": true, "server_name": sni},
				"transport": map[string]any{"type": "ws", "path": wsPath, "host": "a.example"},
			}})
	}
	if err := WritePool(path, nodes, []settings.Policy{{ID: "main", Mode: "best", SelectionOrder: []string{"all:*"}}}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pool healthPool
	if err := json.Unmarshal(body, &pool); err != nil {
		t.Fatal(err)
	}
	contract := pool.HealthPolicies["resproxy-main"]
	fingerprints := map[string]bool{}
	for _, node := range nodes {
		fingerprint := contract.Nodes[node.ID].Fingerprint
		if fingerprint == "" || fingerprints[fingerprint] {
			t.Fatal("distinct proxy configuration shares HTTPS evidence")
		}
		fingerprints[fingerprint] = true
	}
	tcpCalls := 0
	opened, closed := emergencyTCPPreflight(context.Background(), contract.Candidates, pool.DialTargets, 10,
		func(context.Context, string) error { tcpCalls++; return nil })
	if tcpCalls != 1 || len(opened) != len(nodes) || len(closed) != 0 {
		t.Fatal("shared transport listener was not checked exactly once")
	}
	runtime := &fakeSelectorRuntime{probes: map[string]probeEvidence{"working": successfulEvidence(100)}}
	controller := &healthController{runtime: runtime}
	controller.beginProbeBudget(pool, []string{"resproxy-main"})
	results := controller.sharedQualityBatch("resproxy-main", contract, opened)
	if len(runtime.probeCalls) != len(nodes) || !results["working"].OK {
		t.Fatal("per-node HTTPS checks missing")
	}
	for _, id := range []string{"other-uuid", "other-sni", "other-path"} {
		if results[id].OK {
			t.Fatal("open TCP listener or another node's success made a failed proxy healthy")
		}
	}
}
