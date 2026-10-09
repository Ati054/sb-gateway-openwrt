package daemon

import (
	"context"
	"errors"
	"example.org/resilient-proxy/internal/xray"
	"testing"
	"time"
)

func TestWaitAPICompletesWhenReadyWithoutWaitingForBudget(t *testing.T) {
	manager := xray.NewManager("xray", "test", t.TempDir())
	calls := 0
	manager.Run = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		if calls < 2 {
			return nil, errors.New("compiling")
		}
		return []byte("Selecting Override:\n  - block\n- Selects:\n"), nil
	}
	started := time.Now()
	if err := waitAPI(context.Background(), manager, "resproxy-main", make(chan error), 330*time.Second); err != nil {
		t.Fatal(err)
	}
	if calls < 2 || time.Since(started) > time.Second {
		t.Fatal("readiness waited for its large deadline")
	}
}

func TestWaitAPIHonorsCancellationDuringCompilation(t *testing.T) {
	manager := xray.NewManager("xray", "test", t.TempDir())
	manager.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("compiling") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitAPI(ctx, manager, "resproxy-main", make(chan error), 330*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
