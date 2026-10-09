package router

import (
	"context"
	"errors"
	"testing"
)

func TestSBCancelledRoutingCannotPublish(t *testing.T) {
	r := new(Router)
	if err := r.Init(context.Background(), sbReloadConfig(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.SetOverrideTarget("policy", "node-a"); err != nil {
		t.Fatal(err)
	}
	before := r.balancers.Load()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.ReloadRulesContext(ctx, &Config{}, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reload returned %v", err)
	}
	if before != r.balancers.Load() {
		t.Fatal("cancelled reload published a table")
	}
	if got, err := r.GetOverrideTarget("policy"); err != nil || got != "node-a" {
		t.Fatalf("selection changed %q %v", got, err)
	}
}
