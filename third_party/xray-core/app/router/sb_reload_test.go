package router

import (
	"context"
	"sync"
	"testing"
	"time"
)

func sbReloadConfig() *Config {
	return &Config{BalancingRule: []*BalancingRule{{Tag: "policy", OutboundSelector: []string{"node-"}}}}
}

func TestSBOverrideDoesNotWaitForRoutingCompilation(t *testing.T) {
	r := new(Router)
	if err := r.Init(context.Background(), sbReloadConfig(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	done := make(chan error, 1)
	go func() {
		if err := r.SetOverrideTarget("policy", "node-b"); err != nil {
			done <- err
			return
		}
		_, err := r.GetOverrideTarget("policy")
		done <- err
	}()
	select {
	case err := <-done:
		r.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		r.mu.Unlock()
		<-done
		t.Fatal("selector API blocked behind routing compilation")
	}
}

func TestSBReloadPreservesOverrideAndRemoval(t *testing.T) {
	r := new(Router)
	if err := r.Init(context.Background(), sbReloadConfig(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"node-a", "node-b", ""} {
		if err := r.SetOverrideTarget("policy", selected); err != nil {
			t.Fatal(err)
		}
		if err := r.ReloadRules(sbReloadConfig(), false); err != nil {
			t.Fatal(err)
		}
		if got, err := r.GetOverrideTarget("policy"); err != nil || got != selected {
			t.Fatalf("reload override=%q err=%v, want %q", got, err, selected)
		}
	}
	if err := r.ReloadRules(&Config{}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetOverrideTarget("policy"); err == nil {
		t.Fatal("removed balancer survived reload")
	}
}

func TestSBReloadAndOverrideAreSerialized(t *testing.T) {
	r := new(Router)
	if err := r.Init(context.Background(), sbReloadConfig(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for range 200 {
			if err := r.SetOverrideTarget("policy", "node-a"); err != nil {
				t.Error(err)
			}
		}
	}()
	for range 200 {
		if err := r.ReloadRules(sbReloadConfig(), false); err != nil {
			t.Error(err)
		}
	}
	workers.Wait()
	if got, err := r.GetOverrideTarget("policy"); err != nil || got != "node-a" {
		t.Fatalf("concurrent reload lost selected override: %q %v", got, err)
	}
}
