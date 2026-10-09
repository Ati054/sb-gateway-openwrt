package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type retirementFake struct {
	selected string
	retired  []string
}

func (f *retirementFake) Current(context.Context, string) (string, error) {
	return f.selected, nil
}

func (f *retirementFake) Retire(_ context.Context, _, tag string) error {
	f.retired = append(f.retired, tag)
	return nil
}

func TestRetirementKeepsPriorConnectionsForGracePeriod(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	q := &retirementQueue{}
	q.Observe("block", now)
	q.Observe("old-handler", now)
	q.Observe("old-handler", now)
	fake := &retirementFake{selected: "new-handler"}
	if err := q.Sweep(context.Background(), now.Add(20*time.Minute), "resproxy-main", fake); err != nil {
		t.Fatal(err)
	}
	if len(q.entries) != 1 || len(fake.retired) != 0 {
		t.Fatalf("old handler retired before grace: queue=%v retired=%v", q.entries, fake.retired)
	}
	if err := q.Sweep(context.Background(), now.Add(31*time.Minute), "resproxy-main", fake); err != nil {
		t.Fatal(err)
	}
	if len(q.entries) != 0 || len(fake.retired) != 1 || fake.retired[0] != "old-handler" {
		t.Fatalf("old handler not retired after grace: queue=%v retired=%v", q.entries, fake.retired)
	}
}

func TestRetirementBoundsRapidEndpointChurn(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	q := &retirementQueue{}
	for index := 0; index < maxDrainingOutbounds+1; index++ {
		q.Observe(fmt.Sprintf("old-%d", index), now.Add(time.Duration(index)*time.Second))
	}
	fake := &retirementFake{selected: "new-handler"}
	if err := q.Sweep(context.Background(), now.Add(time.Minute), "resproxy-main", fake); err != nil {
		t.Fatal(err)
	}
	if len(q.entries) != maxDrainingOutbounds-3 || len(fake.retired) != 4 || fake.retired[0] != "old-0" {
		t.Fatalf("churn was not bounded: queue=%v retired=%v", q.entries, fake.retired)
	}
}

func TestFailedRetirementStopsNewSubscriptionGenerations(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	q := &retirementQueue{}
	for index := 0; index < maxDrainingOutbounds; index++ {
		q.Observe(fmt.Sprintf("old-%d", index), now)
	}
	if q.HasCapacity() {
		t.Fatal("full outbound backlog admitted another generation")
	}
	q.entries = q.entries[1:]
	if !q.HasCapacity() {
		t.Fatal("confirmed cleanup did not reopen capacity")
	}
}

func TestRetirementNeverRemovesReselectedHandler(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	q := &retirementQueue{}
	q.Observe("active-again", now)
	fake := &retirementFake{selected: "active-again"}
	if err := q.Sweep(context.Background(), now.Add(time.Hour), "resproxy-main", fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.retired) != 0 || len(q.entries) != 1 || !q.entries[0].addedAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("active handler was retired or grace not renewed: %+v %v", q.entries, fake.retired)
	}
}
