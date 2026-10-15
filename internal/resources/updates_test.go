package resources

import (
	"context"
	"testing"
	"time"
)

func TestHeavyUpdatesSerializeAndCancellationDoesNotLeakLease(t *testing.T) {
	first, err := BeginUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !UpdateInProgress() {
		t.Fatal("lease not visible")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, err := BeginUpdate(ctx); err == nil {
		release()
		t.Fatal("overlapping heavy work admitted")
	}
	first()
	if UpdateInProgress() {
		t.Fatal("lease retained")
	}
	second, err := BeginUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second()
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if release, err := BeginUpdate(canceled); err == nil {
		release()
		t.Fatal("canceled work admitted")
	}
}
