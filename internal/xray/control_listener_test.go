package xray

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestControlAPIWorksWithAllProxyTrafficBlocked(t *testing.T) {
	body := testBase(t)
	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	// The control connection must never depend on an exception in user routing.
	config["routing"].(map[string]any)["rules"] = []any{map[string]any{
		"type": "field", "network": "tcp,udp", "outboundTag": "block",
	}}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := StartEmbedded(body, testAPIAddress(t, body))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	// Use a fresh connection too: an already established old API session
	// could otherwise hide dependence on a removed routing exception.
	client, err := NewAPIClient(testAPIAddress(t, body))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager := NewManager("unused", testAPIAddress(t, body), t.TempDir())
	manager.Run = client.Command
	if err := manager.CheckAPI(ctx); err != nil {
		t.Fatalf("proxy blocking disabled local control API: %v", err)
	}
}

func testAPIAddress(t testing.TB, body []byte) string {
	t.Helper()
	var config struct {
		API struct {
			Listen string `json:"listen"`
		} `json:"api"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	if config.API.Listen == "" {
		t.Fatal("missing dedicated API address")
	}
	return config.API.Listen
}
