package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRouterDNSWaitsForRestartedListener(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := waitRouterDNS(ctx, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("connection refused")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatal("dnsmasq startup caused premature rollback", attempts, err)
	}
}

func TestRouterDNSReadyPathHasNoRetry(t *testing.T) {
	attempts := 0
	if err := waitRouterDNS(context.Background(), func(context.Context) error {
		attempts++
		return nil
	}); err != nil || attempts != 1 {
		t.Fatal(attempts, err)
	}
}

func TestRouterDNSFailureHonorsDeadlineAndPreservesCause(t *testing.T) {
	cause := errors.New("connection refused")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := waitRouterDNS(ctx, func(context.Context) error { return cause })
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, cause) || time.Since(started) > time.Second {
		t.Fatal("DNS rollback did not respect its deadline", err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if err := waitRouterDNS(canceled, func(context.Context) error {
		t.Fatal("lookup started after cancellation")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
