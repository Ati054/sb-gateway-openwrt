package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"encoding/json"
	"example.org/resilient-proxy/internal/catalog"
	"example.org/resilient-proxy/internal/health"
	"example.org/resilient-proxy/internal/settings"
	"example.org/resilient-proxy/internal/xray"
)

func reloadFixture() settings.Config {
	return settings.Config{Enabled: true, RouteEnabled: true, Base: xray.DefaultBaseConfig(), Policies: []settings.Policy{{ID: "main", Mode: "best", SelectionOrder: []string{"country:CA"}, Health: health.Policy{ActiveLivenessSeconds: 3}}}}
}

func TestMonitoringReloadPreservesCatalogAndChangesOnlyHealth(t *testing.T) {
	previous := reloadFixture()
	next := reloadFixture()
	next.Policies[0].Health.ActiveLivenessSeconds = 5
	next.Policies[0].Health.HistoryLimitKiB = 256
	path := filepath.Join(t.TempDir(), "pool.json")
	nodes := []catalog.Node{{ID: "node", ProviderID: "fixture", Label: "fixture", Country: "CA", Protocol: "vless", Transport: "tcp", Server: "127.0.0.1", Port: 443, Credentials: map[string]string{"uuid": "11111111-1111-4111-8111-111111111111"}}}
	if err := applyMonitoringReload(context.Background(), previous, next, nodes, path); err != nil {
		t.Fatal(err)
	}
	var pool struct {
		Outbounds map[string]any `json:"outbounds"`
		Policies  map[string]struct {
			Policy struct {
				Liveness     int `json:"active_liveness_interval_seconds"`
				HistoryLimit int `json:"history_limit_kib"`
			} `json:"policy"`
		} `json:"health_policies"`
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(body, &pool); err != nil {
		t.Fatal(err)
	}
	if len(pool.Outbounds) != 1 || pool.Policies["resproxy-main"].Policy.Liveness != 5 || pool.Policies["resproxy-main"].Policy.HistoryLimit != 256 || previous.Policies[0].Health.ActiveLivenessSeconds != 3 {
		t.Fatalf("reload changed catalog/previous config: %s", body)
	}
}

func TestStructuralReloadRequiresRestartWithoutPublishing(t *testing.T) {
	for _, change := range []func(*settings.Config){
		func(c *settings.Config) { c.DNSMode = "proxy-only" },
		func(c *settings.Config) { c.Base.TransparentPort++ },
		func(c *settings.Config) { c.Policies[0].SelectionOrder = []string{"country:NL"} },
		func(c *settings.Config) { c.RouteEnabled = false },
		func(c *settings.Config) { c.Capture.IPv6Mode = "direct" },
		func(c *settings.Config) { c.Capture.DirectIPs = []string{"203.0.113.4/32"} },
		func(c *settings.Config) { c.Policies[0].NodeFilter.Include = "premium" },
	} {
		previous := reloadFixture()
		next := reloadFixture()
		change(&next)
		path := filepath.Join(t.TempDir(), "pool.json")
		err := applyMonitoringReload(context.Background(), previous, next, nil, path)
		if !errors.Is(err, ErrRestartRequired) {
			t.Fatalf("structural change accepted: %v", err)
		}
		if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("structural reload published health contract")
		}
	}
}

func TestExpiredReloadDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	previous := reloadFixture()
	next := reloadFixture()
	next.Policies[0].Health.ActiveLivenessSeconds = 5
	path := filepath.Join(t.TempDir(), "pool.json")
	if err := applyMonitoringReload(ctx, previous, next, nil, path); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired request wrote pool")
	}
}

func TestUnchangedReloadDoesNotRewritePool(t *testing.T) {
	config := reloadFixture()
	path := filepath.Join(t.TempDir(), "pool.json")
	if err := applyMonitoringReload(context.Background(), config, config, nil, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unchanged reload rewrote pool")
	}
}

func TestReloadSocketRequiresExplicitRestartResponse(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix sockets are exercised on Linux and ARM")
	}
	paths := Paths{RunRoot: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests, stop, err := startReloadServer(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	go func() { request := <-requests; request.reply <- reloadResult{Status: "restart"} }()
	call, cancelCall := context.WithTimeout(ctx, 2*time.Second)
	defer cancelCall()
	if err := RequestReload(call, paths); !errors.Is(err, ErrRestartRequired) {
		t.Fatal(err)
	}
}

func TestCaptureOnlyReloadClassification(t *testing.T) {
	previous, next := reloadFixture(), reloadFixture()
	next.Capture.IPv6Mode = "block"
	next.Capture.DirectIPs = []string{"203.0.113.4/32"}
	if !captureOnly(previous, next) {
		t.Fatal("capture edits require an unnecessary restart")
	}
	next.DNSMode = "proxy-only"
	if captureOnly(previous, next) {
		t.Fatal("DNS edit incorrectly applied as capture-only")
	}
	next = reloadFixture()
	next.Policies[0].Health.ActiveLivenessSeconds++
	if captureOnly(previous, next) {
		t.Fatal("health edit incorrectly applied as capture-only")
	}
}
