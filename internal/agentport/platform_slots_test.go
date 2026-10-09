package agentport

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestLiveAPICommandDoesNotWaitForFullBackgroundSlots(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(xrayProbeCommandSlots); i++ {
		xrayProbeCommandSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(xrayProbeCommandSlots); i++ {
			<-xrayProbeCommandSlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, err := runXrayCommand(ctx, 3*time.Second, binary, "-test.run=^$"); err != nil {
		t.Fatalf("live API waited behind background preparation: %v", err)
	}
	blocked, cancelBlocked := context.WithTimeout(backgroundXrayCommandContext(context.Background()), 20*time.Millisecond)
	defer cancelBlocked()
	if _, err := runXrayCommand(blocked, time.Second, binary, "-test.run=^$"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued background cancellation lost: %v", err)
	}
	if len(xrayCommandSlot) != 0 || len(xrayProbeCommandSlots) != 2 {
		t.Fatal("command leaked or stole a slot")
	}
}
