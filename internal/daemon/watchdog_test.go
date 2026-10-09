package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWatchdogProgressUsesMonotonicAge(t *testing.T) {
	p := newHealthProgress()
	if p.fresh(0) {
		t.Fatal("unstarted monitor renewed lease")
	}
	p.mark()
	at := time.Duration(p.elapsed.Load())
	if !p.fresh(at+29*time.Second) || p.fresh(at+31*time.Second) || p.fresh(at-time.Second) {
		t.Fatal("wrong progress age gate")
	}
}

func TestCriticalWorkerExitCancelsSlowOwnerWork(t *testing.T) {
	for _, failure := range []error{nil, errors.New("worker unavailable")} {
		ctx, cancel := context.WithCancelCause(context.Background())
		superviseCritical(ctx, cancel, "health", func(context.Context) error { return failure })
		if ctx.Err() == nil {
			t.Fatal("owner stayed alive without its critical worker")
		}
		if failure != nil && !errors.Is(context.Cause(ctx), failure) {
			t.Fatalf("failure lost: %v", context.Cause(ctx))
		}
	}
	parent, stop := context.WithCancel(context.Background())
	stop()
	ctx, cancel := context.WithCancelCause(parent)
	superviseCritical(ctx, cancel, "health", func(context.Context) error { return errors.New("shutdown") })
	if context.Cause(ctx) != context.Canceled {
		t.Fatal("normal shutdown replaced by false failure")
	}
}

func TestWatchdogOnlyRenewsWithResponsiveCoreAndProgress(t *testing.T) {
	for _, healthy := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		p := newHealthProgress()
		p.mark()
		feeds := 0
		watchProcd(ctx, "gateway", p, func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("watchdog check has no deadline")
			}
			if !healthy {
				cancel()
				return errors.New("core stalled")
			}
			return nil
		}, func(context.Context) error { feeds++; cancel(); return nil })
		if (feeds == 1) != healthy {
			t.Fatalf("healthy=%v feeds=%d", healthy, feeds)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchProcd(ctx, "other-service", newHealthProgress(), func(context.Context) error { t.Fatal("touched unrelated instance"); return nil }, func(context.Context) error { t.Fatal("touched unrelated watchdog"); return nil })
}
