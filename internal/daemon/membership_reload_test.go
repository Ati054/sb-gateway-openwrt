package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/Ati054/xroute-openwrt/internal/agentport"
	"github.com/Ati054/xroute-openwrt/internal/catalog"
	"github.com/Ati054/xroute-openwrt/internal/settings"
)

func TestMembershipApplyDoesNotRequestServiceRestart(t *testing.T) {
	previous, next := reloadFixture(), reloadFixture()
	next.Policies[0].SelectionOrder = []string{"country:CA", "country:NL"}
	if result, handled := classifyControlReload(previous, next); handled {
		t.Fatalf("membership edit cannot reach hot apply: %+v", result)
	}
}

func membershipNodes() []catalog.Node {
	nodes := []catalog.Node{
		{ID: "active", Country: "CA", Label: "active"},
		{ID: "reserve", Country: "NL", Label: "reserve"},
	}
	for i := range nodes {
		nodes[i].ProviderID = "fixture"
		nodes[i].Protocol, nodes[i].Transport = "vless", "tcp"
		nodes[i].Server, nodes[i].Port = "127.0.0.1", 443
		nodes[i].Credentials = map[string]string{"uuid": "11111111-1111-4111-8111-111111111111"}
	}
	return nodes
}

func poolMembers(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pool struct {
		Policies map[string][]string `json:"policies"`
	}
	if err := json.Unmarshal(body, &pool); err != nil {
		t.Fatal(err)
	}
	return pool.Policies["resproxy-main"]
}

func TestMembershipReloadPublishesAddedAndRemovedReserve(t *testing.T) {
	previous, next := reloadFixture(), reloadFixture()
	nodes := membershipNodes()
	path := filepath.Join(t.TempDir(), "pool.json")
	if err := agentport.WritePool(path, nodes, previous.Policies); err != nil {
		t.Fatal(err)
	}
	next.Policies[0].SelectionOrder = []string{"country:CA", "country:NL"}
	if err := applyMonitoringReload(context.Background(), previous, next, nodes, path); err != nil {
		t.Fatal(err)
	}
	if got := poolMembers(t, path); !reflect.DeepEqual(got, []string{"active", "reserve"}) {
		t.Fatal(got)
	}
	if err := applyMonitoringReload(context.Background(), next, previous, nodes, path); err != nil {
		t.Fatal(err)
	}
	if got := poolMembers(t, path); !reflect.DeepEqual(got, []string{"active"}) {
		t.Fatal(got)
	}
}

// Keep structural edits out of the pool-only path, including mixed changes.
func TestMembershipDoesNotHideStructuralChanges(t *testing.T) {
	for _, edit := range []func(*settings.Config){
		func(c *settings.Config) { c.Policies[0].Mode = "priority" },
		func(c *settings.Config) { c.Policies[0].ID = "other" },
		func(c *settings.Config) { c.Policies[0].RuleRefs = []string{"ru-all"} },
		func(c *settings.Config) { c.Policies[0].SourceCIDRs = []string{"192.0.2.1/32"} },
		func(c *settings.Config) { c.Policies[0].WANFallback = true },
		func(c *settings.Config) { c.DNSPort++ },
	} {
		previous, next := reloadFixture(), reloadFixture()
		next.Policies[0].SelectionOrder = []string{"country:NL"}
		edit(&next)
		if result, handled := classifyControlReload(previous, next); !handled || result.Status != "restart" {
			t.Fatalf("structural edit accepted: %+v", result)
		}
	}
}

func TestMembershipApplySurvivesConcurrentCatalogPublicationAndRollback(t *testing.T) {
	previous, next := reloadFixture(), reloadFixture()
	next.Policies[0].SelectionOrder = []string{"country:CA", "country:NL"}
	next.Policies[0].Health.ActiveLivenessSeconds = 5
	oldNodes := membershipNodes()
	freshNodes := membershipNodes()
	freshNodes[1].ID = "fresh-reserve"
	path := filepath.Join(t.TempDir(), "pool.json")
	if err := agentport.WritePool(path, oldNodes, previous.Policies); err != nil {
		t.Fatal(err)
	}
	c := newSubscriptionControl(previous)
	c.poolNodes = oldNodes
	c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
	c.applyReload = func(ctx context.Context, old, next settings.Config) error {
		return c.reloadPoolLocked(ctx, path, old, next)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c.preparePool = func(nodes []catalog.Node, policies []settings.Policy) (*agentport.PreparedPool, error) {
		once.Do(func() { close(entered); <-resume })
		return agentport.PreparePool(nodes, policies)
	}
	finished := make(chan error, 1)
	go func() { finished <- c.publishPool(path, freshNodes) }()
	<-entered
	// Apply must use the published old inventory, not the catalog being prepared.
	result, handled := c.handle(reloadRequest{ctx: t.Context()})
	close(resume)
	err := <-finished
	if !handled || result.Status != "applied" {
		t.Fatalf("Apply failed: %+v", result)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := poolMembers(t, path); !reflect.DeepEqual(got, []string{"active", "fresh-reserve"}) {
		t.Fatalf("stale selection won publication: %v", got)
	}
	if c.poolNodes[1].ID != "fresh-reserve" {
		t.Fatal("published inventory was not advanced")
	}
	// A failed subscription transaction rolls back its catalog, not user choices.
	if err := c.publishPool(path, oldNodes); err != nil {
		t.Fatal(err)
	}
	if got := poolMembers(t, path); !reflect.DeepEqual(got, []string{"active", "reserve"}) {
		t.Fatal(got)
	}
	if c.snapshot().Policies[0].Health.ActiveLivenessSeconds != 5 {
		t.Fatal("health edit was lost")
	}
	// A later edit must now use the rollback inventory, not freshNodes.
	next.Policies = append([]settings.Policy(nil), next.Policies...)
	next.Policies[0].SelectionOrder = []string{"country:NL"}
	if result, _ := c.handle(reloadRequest{ctx: t.Context()}); result.Status != "applied" {
		t.Fatal(result)
	}
	if got := poolMembers(t, path); !reflect.DeepEqual(got, []string{"reserve"}) {
		t.Fatal(got)
	}
}

func TestFailedMembershipApplyPreservesAcknowledgedPoolAndConfig(t *testing.T) {
	previous, next := reloadFixture(), reloadFixture()
	nodes := membershipNodes()
	path := filepath.Join(t.TempDir(), "pool.json")
	if err := agentport.WritePool(path, nodes, previous.Policies); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	c := newSubscriptionControl(previous)
	c.poolNodes = nodes
	c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
	c.applyReload = func(ctx context.Context, old, next settings.Config) error {
		return c.reloadPoolLocked(ctx, path, old, next)
	}
	next.Policies[0].SelectionOrder = []string{"invalid-selector"}
	if result, _ := c.handle(reloadRequest{ctx: t.Context()}); result.Status != "error" {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(c.snapshot(), previous) {
		t.Fatal("invalid selection committed")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid selection rewrote pool")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	next = reloadFixture()
	next.Policies[0].SelectionOrder = []string{"country:NL"}
	if err := applyMonitoringReload(ctx, previous, next, nodes, path); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled selection rewrote pool")
	}
}
