package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"example.org/resilient-proxy/internal/agentport"
	"example.org/resilient-proxy/internal/catalog"
	"example.org/resilient-proxy/internal/settings"
)

func TestMonitoringApplyRespondsWhileSubscriptionLoopIsBusy(t *testing.T) {
	current, next := reloadFixture(), reloadFixture()
	next.Policies[0].Health.ActiveLivenessSeconds = 5
	root := t.TempDir()
	path := filepath.Join(root, "pool.json")
	node := catalog.Node{ID: "new-node", ProviderID: "fixture", Label: "fresh catalog", Country: "CA", Protocol: "vless", Transport: "tcp", Server: "127.0.0.1", Port: 443, Credentials: map[string]string{"uuid": "11111111-1111-4111-8111-111111111111"}}
	if err := agentport.WritePool(path, []catalog.Node{node}, current.Policies); err != nil {
		t.Fatal(err)
	}
	c := newSubscriptionControl(current)
	c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
	c.applyReload = func(ctx context.Context, old, next settings.Config) error {
		return agentport.UpdatePoolHealth(ctx, path, next.Policies)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests, stop, err := startReloadServer(ctx, Paths{RunRoot: root}, c.handle)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// No subscriber reads requests: this models a loop occupied by slow probes.
	// A full catalog publication is preparing concurrently. Delay that CPU
	// work deterministically and prove Apply is not waiting behind its lock.
	entered, resume := make(chan struct{}), make(chan struct{})
	c.preparePool = func(nodes []catalog.Node, policies []settings.Policy) (*agentport.PreparedPool, error) {
		close(entered)
		<-resume
		return agentport.PreparePool(nodes, policies)
	}
	published := make(chan error, 1)
	go func() { published <- c.publishPool(path, []catalog.Node{node}) }()
	<-entered
	var check func()
	defer func() {
		close(resume)
		if err := <-published; err != nil {
			t.Error(err)
		}
		if check != nil {
			check()
		}
	}()
	callCtx, release := context.WithTimeout(ctx, 2*time.Second)
	defer release()
	if err := RequestReload(callCtx, Paths{RunRoot: root}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requests:
		t.Fatal("monitoring was queued behind subscription work")
	default:
	}
	if c.snapshot().Policies[0].Health.ActiveLivenessSeconds != 5 {
		t.Fatal("control configuration was not committed")
	}
	check = func() {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var pool struct {
			Outbounds map[string]any `json:"outbounds"`
			Policies  map[string]struct {
				Policy struct {
					Interval int `json:"active_liveness_interval_seconds"`
				} `json:"policy"`
			} `json:"health_policies"`
		}
		if err := json.Unmarshal(body, &pool); err != nil {
			t.Fatal(err)
		}
		if len(pool.Outbounds) != 1 || pool.Outbounds[node.ID] == nil || pool.Policies["resproxy-main"].Policy.Interval != 5 {
			t.Fatalf("catalog or new policy lost: %s", body)
		}
	}
	check()
	// Subsequent subscription publication and rollback use the latest policy.
	prepared, err := agentport.PreparePool([]catalog.Node{node}, current.Policies)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Publish(path, c.snapshot().Policies); err != nil {
		t.Fatal(err)
	}
	check()
}
