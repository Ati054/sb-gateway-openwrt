package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"example.org/resilient-proxy/internal/settings"
)

func TestStartupReadinessRequiresAllCurrentSelections(t *testing.T) {
	config := settings.Config{Policies: []settings.Policy{{ID: "all"}, {ID: "pc"}}}
	for _, tc := range []struct {
		body  string
		ready bool
	}{
		{`{}`, false},
		{`{"resproxy-all":{"selected":"a","runtime_selected":"a","runtime_confirmed":true}}`, false},
		{`{"resproxy-all":{"selected":"a","runtime_selected":"a","runtime_confirmed":true},"resproxy-pc":{"selected":"b","runtime_selected":"b","runtime_confirmed":false}}`, false},
		{`{"resproxy-all":{"selected":"a","runtime_selected":"a","runtime_confirmed":true},"resproxy-pc":{"selected":"b","runtime_selected":"block","runtime_confirmed":true}}`, false},
		{`{"resproxy-all":{"selected":"a","runtime_selected":"a","runtime_confirmed":true},"resproxy-pc":{"selected":"b","runtime_selected":"b","runtime_confirmed":true}}`, true},
		{`{"resproxy-all":{"selected":"block","runtime_selected":"block","runtime_confirmed":true},"resproxy-pc":{"selected":"block","runtime_selected":"block","runtime_confirmed":true}}`, true},
		{`bad-json`, false},
	} {
		if got := startupRoutesConfirmed([]byte(tc.body), config); got != tc.ready {
			t.Fatalf("ready=%v for %s", got, tc.body)
		}
	}
}

func TestStartupWaitCancelsAndAcceptsConfirmedBlock(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	config := settings.Config{Policies: []settings.Policy{{ID: "all"}}}
	core, health := make(chan error), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitStartupRoutes(ctx, config, paths, core, health); err == nil {
		t.Fatal("missing routes reported ready")
	}
	close(core)
	if err := waitStartupRoutes(context.Background(), config, paths, core, health); err == nil {
		t.Fatal("dead core reported ready")
	}
	if err := os.MkdirAll(filepath.Join(paths.RunRoot, "health-state"), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"resproxy-all":{"selected":"block","runtime_selected":"block","runtime_confirmed":true}}`)
	if err := os.WriteFile(filepath.Join(paths.RunRoot, "health-state", "selector-health.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := waitStartupRoutes(context.Background(), config, paths, make(chan error), health); err != nil {
		t.Fatal("confirmed outage must still allow startup:", err)
	}
}

func TestStartupWaitIncludesConfiguredWANFallback(t *testing.T) {
	config := settings.Config{Policies: []settings.Policy{{ID: "all", WANFallback: true}}}
	blocked := []byte(`{"resproxy-all":{"selected":"block","runtime_selected":"block","runtime_confirmed":true}}`)
	if startupRoutesConfirmed(blocked, config) {
		t.Fatal("startup completed before configured WAN fallback")
	}
	active := []byte(`{"resproxy-all":{"selected":"block","runtime_selected":"block","runtime_confirmed":true,"wan_fallback_active":true}}`)
	if !startupRoutesConfirmed(active, config) {
		t.Fatal("confirmed WAN fallback was not ready")
	}
}
