package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type routingFake struct {
	overrides map[string]string
	base      map[string]string
	applied   [][]byte
	failNext  bool
}

func (fake *routingFake) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) < 2 || args[0] != "api" {
		return nil, errors.New("unexpected command")
	}
	switch args[1] {
	case "bi":
		tag := args[len(args)-1]
		return []byte("Selecting Override:\n  - " + fake.overrides[tag] + "\n- Selects: " + fake.base[tag] + "\n"), nil
	case "bo":
		fake.overrides[args[len(args)-2]] = args[len(args)-1]
		return nil, nil
	case "adrules":
		body, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		var document struct {
			Routing struct {
				Balancers []struct {
					Tag      string   `json:"tag"`
					Selector []string `json:"selector"`
				} `json:"balancers"`
			} `json:"routing"`
		}
		if err := json.Unmarshal(body, &document); err != nil {
			return nil, err
		}
		if fake.failNext {
			fake.failNext = false
			return nil, errors.New("simulated reload rejection")
		}
		fake.applied = append(fake.applied, body)
		for _, balancer := range document.Routing.Balancers {
			// The pinned core preserves the latest override during publication.
			fake.base[balancer.Tag] = balancer.Selector[0]
		}
		return nil, nil
	}
	return nil, errors.New("unexpected action")
}

func TestRoutingPublicationPreservesLiveSelectorThroughReload(t *testing.T) {
	previous := DefaultBaseConfig()
	next := previous
	next.Policies = []BasePolicy{{ID: "main", Rules: []RouteRule{{ID: "government", Action: "direct", Domains: []string{"gosuslugi.ru"}}}}}
	fake := &routingFake{overrides: map[string]string{"resproxy-main": "resproxy-main-active"}, base: map[string]string{}}
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	manager.Run = fake.run
	if err := manager.PublishRouting(context.Background(), previous, next); err != nil {
		t.Fatal(err)
	}
	if len(fake.applied) != 1 || fake.base["resproxy-main"] != "resproxy-main-active" ||
		fake.overrides["resproxy-main"] != "resproxy-main-active" ||
		!strings.Contains(string(fake.applied[0]), "domain:gosuslugi.ru") {
		t.Fatalf("routing reload lost the live handler: base=%v overrides=%v", fake.base, fake.overrides)
	}
}

func TestRoutingPublicationRollsBackRejectedUpdate(t *testing.T) {
	previous := DefaultBaseConfig()
	next := previous
	next.Policies = []BasePolicy{{ID: "main", Rules: []RouteRule{{ID: "government", Action: "direct", Domains: []string{"gosuslugi.ru"}}}}}
	fake := &routingFake{overrides: map[string]string{"resproxy-main": "resproxy-main-active"}, base: map[string]string{}, failNext: true}
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	manager.Run = fake.run
	if err := manager.PublishRouting(context.Background(), previous, next); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("expected confirmed rollback, got %v", err)
	}
	if len(fake.applied) != 1 || strings.Contains(string(fake.applied[0]), "gosuslugi.ru") ||
		fake.overrides["resproxy-main"] != "resproxy-main-active" {
		t.Fatalf("previous route was not restored: base=%v overrides=%v", fake.base, fake.overrides)
	}
}

func TestRoutingPublicationAllowsLargeRuleCompilation(t *testing.T) {
	previous := DefaultBaseConfig()
	next := previous
	next.Policies = []BasePolicy{{ID: "main", Rules: []RouteRule{{ID: "country", Action: "direct", Country: "ru", UseGeoIP: true, CountryCIDRs: []string{"203.0.113.0/24"}}}}}
	fake := &routingFake{overrides: map[string]string{"resproxy-main": "resproxy-main-active"}, base: map[string]string{}}
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	manager.Run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "adrules" {
			if !strings.Contains(strings.Join(args, " "), "--timeout=25") {
				return nil, errors.New("adrules kept its three-second RPC deadline")
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) < 25*time.Second {
				return nil, errors.New("large routing command received selector deadline")
			}
		}
		return fake.run(ctx, binary, args...)
	}
	if err := manager.PublishRouting(context.Background(), previous, next); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingRollbackSurvivesCancelledUpdate(t *testing.T) {
	previous := DefaultBaseConfig()
	next := previous
	next.Policies = []BasePolicy{{ID: "main", Rules: []RouteRule{{ID: "example", Action: "direct", Domains: []string{"example.com"}}}}}
	fake := &routingFake{overrides: map[string]string{"resproxy-main": "resproxy-main-active"}, base: map[string]string{}}
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := true
	manager.Run = func(call context.Context, binary string, args ...string) ([]byte, error) {
		if call.Err() != nil {
			return nil, call.Err()
		}
		if args[1] == "adrules" && first {
			first = false
			cancel()
			return nil, context.Canceled
		}
		return fake.run(call, binary, args...)
	}
	err := manager.PublishRouting(ctx, previous, next)
	if err == nil || !strings.Contains(err.Error(), "rolled back") || len(fake.applied) != 1 {
		t.Fatalf("cancelled update did not restore routing: %v, applied %d", err, len(fake.applied))
	}
}

func TestUnchangedRoutingNeedsNoCoreMutation(t *testing.T) {
	config := DefaultBaseConfig()
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	manager.Run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unchanged routing called core")
		return nil, nil
	}
	if err := manager.PublishRouting(context.Background(), config, config); err != nil {
		t.Fatal(err)
	}
}

func TestLargeCIDRRoutingReceivesBoundedBudget(t *testing.T) {
	previous := DefaultBaseConfig()
	next := DefaultBaseConfig()
	cidrs := make([]string, 4097)
	for i := range cidrs {
		cidrs[i] = "203.0.113.0/24"
	}
	next.Policies[0].Rules = []RouteRule{{ID: "ru", Action: "direct", Country: "ru", UseGeoIP: true, CountryCIDRs: cidrs}}
	if StartupTimeout(previous) != 20*time.Second || StartupTimeout(next) != 330*time.Second {
		t.Fatal("wrong startup budgets")
	}
	fake := &routingFake{overrides: map[string]string{"resproxy-main": "resproxy-main-active"}, base: map[string]string{}}
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	manager.Run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if args[1] == "adrules" {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) < 300*time.Second || !strings.Contains(strings.Join(args, " "), "--timeout=300") {
				t.Fatal("large table lacks bounded compilation budget")
			}
		}
		return fake.run(ctx, binary, args...)
	}
	if err := manager.PublishRouting(context.Background(), previous, next); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingCompilationAllowsSwitchAndNeverRestoresOldSnapshot(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprint(rollback), func(t *testing.T) {
			previous := DefaultBaseConfig()
			next := DefaultBaseConfig()
			next.Policies[0].Rules = []RouteRule{{ID: "example", Action: "direct", Domains: []string{"example.com"}}}
			fake := &routingFake{overrides: map[string]string{"resproxy-main": "old"}, base: map[string]string{}}
			manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
			started, release := make(chan struct{}), make(chan struct{})
			first := true
			manager.Run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
				if args[1] == "adrules" && first {
					first = false
					close(started)
					<-release
					if rollback {
						return nil, errors.New("update rejected")
					}
				}
				return fake.run(ctx, binary, args...)
			}
			done := make(chan error, 1)
			go func() { done <- manager.PublishRouting(context.Background(), previous, next) }()
			<-started
			switched := make(chan struct{})
			go func() {
				MutationMu.Lock()
				fake.overrides["resproxy-main"] = "latest"
				MutationMu.Unlock()
				close(switched)
			}()
			select {
			case <-switched:
			case <-time.After(time.Second):
				close(release)
				<-done
				t.Fatal("routing compilation blocked live selector")
			}
			close(release)
			err := <-done
			if rollback && (err == nil || !strings.Contains(err.Error(), "rolled back")) {
				t.Fatalf("rollback not confirmed: %v", err)
			}
			if !rollback && err != nil {
				t.Fatal(err)
			}
			if fake.overrides["resproxy-main"] != "latest" {
				t.Fatal("publication/rollback restored stale override")
			}
		})
	}
}
