package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitialCaptureRetriesWithProtectionIntact(t *testing.T) {
	guard := filepath.Join(t.TempDir(), "guard")
	if err := os.WriteFile(guard, []byte("strict-policy"), 0600); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	err := waitInitialCapture(context.Background(), func(context.Context) error {
		attempts++
		if b, err := os.ReadFile(guard); err != nil || string(b) != "strict-policy" {
			t.Fatal("protection changed", err)
		}
		if attempts < 3 {
			return errors.New("temporary VPN discovery timeout")
		}
		return nil
	}, time.Millisecond)
	if err != nil || attempts != 3 {
		t.Fatal("startup did not recover", attempts, err)
	}
}

func TestInitialCaptureCancellationInterruptsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	start := time.Now()
	err := waitInitialCapture(ctx, func(context.Context) error {
		attempts++
		cancel()
		return errors.New("discovery unavailable")
	}, time.Hour)
	if !errors.Is(err, context.Canceled) || attempts != 1 || time.Since(start) > time.Second {
		t.Fatal("cancellation delayed or retried", attempts, err)
	}
}

func TestInitialCaptureStopsPersistentFailuresAtDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	attempts := 0
	err := waitInitialCapture(ctx, func(context.Context) error { attempts++; return errors.New("not ready") }, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || attempts < 2 {
		t.Fatal(attempts, err)
	}
}
