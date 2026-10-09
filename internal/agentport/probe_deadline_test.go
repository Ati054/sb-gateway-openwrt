package agentport

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestProbeDeadlineSeparatesSchedulingFromRemoteFailure(t *testing.T) {
	start := time.Unix(100, 0)
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		err     error
		local   bool
	}{
		{"prompt remote timeout", 2100 * time.Millisecond, context.DeadlineExceeded, false},
		{"substantially late timeout", 4 * time.Second, context.DeadlineExceeded, true},
		{"deadline processing delayed past successful response", 4 * time.Second, nil, true},
		{"late fatal network failure stays fatal", 4 * time.Second, syscall.ECONNREFUSED, false},
		{"late certificate failure stays fatal", 4 * time.Second, errors.New("certificate rejected"), false},
		{"valid slow response", 2300 * time.Millisecond, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := probeDeadlineResult(context.Background(), start, start.Add(tc.elapsed), 2*time.Second, tc.err)
			if errors.Is(got, errProbeSchedulingDelayed) != tc.local {
				t.Fatalf("local=%v err=%v", tc.local, got)
			}
			if tc.err != nil && !errors.Is(got, tc.err) {
				t.Fatal("original error was lost")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := probeDeadlineResult(ctx, start, start.Add(4*time.Second), 2*time.Second, context.Canceled); got != context.Canceled {
		t.Fatalf("cancellation changed: %v", got)
	}
}
