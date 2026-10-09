package agentport

import (
	"context"
	"testing"
	"time"
)

func TestHealthIdleWaitReportsProgressWithoutStartingProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	marks := 0
	started := time.Now()
	ready := waitForHealthWake(ctx, time.Minute, nil, 10*time.Millisecond, nil, nil, func() { marks++; cancel() })
	if ready || marks < 1 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("ready=%v marks=%d elapsed=%v", ready, marks, time.Since(started))
	}
}
