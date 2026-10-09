package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.org/resilient-proxy/internal/settings"
)

func TestDailyRefreshBoundaryAndRestart(t *testing.T) {
	source := settings.Subscription{ID: "office", UpdateMode: "daily", UpdateTime: "03:15", URL: "https://example.test"}
	local := time.FixedZone("router", 7*3600)
	before := time.Date(2026, 10, 2, 3, 14, 55, 0, local)
	due := before.Add(5 * time.Second)
	schedule := dailyRefreshSchedule{}
	if schedule.due(source, before) || !schedule.due(source, due) {
		t.Fatal("daily boundary incorrect")
	}
	schedule.attempted(source, due) // Failure consumes today's automatic attempt too.
	if schedule.due(source, due.Add(30*time.Second)) || schedule.due(source, due.Add(23*time.Hour)) {
		t.Fatal("repeated automatic attempt on the same day")
	}
	if !schedule.due(source, due.AddDate(0, 0, 1)) {
		t.Fatal("next calendar day did not run")
	}
	restarted := dailyRefreshSchedule{}
	if restarted.due(source, due.Add(time.Hour)) {
		t.Fatal("restart replayed today's schedule")
	}
	if !restarted.due(source, due.AddDate(0, 0, 1)) {
		t.Fatal("restart lost next day's schedule")
	}
	if !sourceRefreshDue(source, due, due.Add(time.Hour), true) {
		t.Fatal("manual request must remain available")
	}
}

func TestDailyRefreshMidnightAndClockChanges(t *testing.T) {
	source := settings.Subscription{ID: "office", UpdateTime: "00:00"}
	now := time.Date(2026, 10, 2, 23, 59, 50, 0, time.UTC)
	schedule := dailyRefreshSchedule{}
	schedule.due(source, now)
	midnight := now.Add(10 * time.Second)
	if !schedule.due(source, midnight) {
		t.Fatal("midnight schedule missing")
	}
	schedule.attempted(source, midnight)
	if schedule.due(source, midnight.Add(-time.Hour)) || schedule.due(source, midnight.Add(time.Hour)) {
		t.Fatal("clock rollback replayed completed date")
	}
	// Spring-forward skips 02:30; run once on the first observation afterwards.
	source.UpdateTime = "02:30"
	schedule = dailyRefreshSchedule{}
	morning := time.Date(2026, 3, 29, 1, 59, 0, 0, time.UTC)
	schedule.due(source, morning)
	if !schedule.due(source, morning.Add(61*time.Minute)) {
		t.Fatal("clock jump skipped today's update")
	}
	schedule.attempted(source, morning.Add(61*time.Minute))
	if schedule.due(source, morning.Add(30*time.Minute)) {
		t.Fatal("repeated time after clock rollback")
	}
}

func TestDailySourcesAreIndependent(t *testing.T) {
	a := settings.Subscription{ID: "a", UpdateTime: "08:00"}
	b := settings.Subscription{ID: "b", UpdateTime: "09:00"}
	schedule := dailyRefreshSchedule{}
	now := time.Date(2026, 10, 2, 7, 59, 0, 0, time.UTC)
	schedule.due(a, now)
	schedule.due(b, now)
	now = now.Add(time.Minute)
	if !schedule.due(a, now) || schedule.due(b, now) {
		t.Fatal("source times mixed")
	}
	schedule.attempted(a, now)
	if !schedule.due(b, now.Add(time.Hour)) {
		t.Fatal("first source consumed second source")
	}
}

func TestRouterClockUsesSystemOffsetAndCaches(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	calls := 0
	clock := routerClock{read: func(context.Context) ([]byte, error) {
		calls++
		return []byte("2026-10-02 05:45:00 +0545\n"), nil
	}}
	got, err := clock.now(ctx, now)
	if err != nil || got.Hour() != 5 || got.Minute() != 45 {
		t.Fatalf("router offset lost: %v %v", got, err)
	}
	got, err = clock.now(ctx, now.Add(20*time.Second))
	if err != nil || got.Second() != 20 || calls != 1 {
		t.Fatal("router clock not cached")
	}
	clock.read = func(context.Context) ([]byte, error) { calls++; return []byte("2026-10-02 07:00:31 +0700"), nil }
	got, err = clock.now(ctx, now.Add(31*time.Second))
	if err != nil || got.Hour() != 7 || calls != 2 {
		t.Fatal("timezone change not followed")
	}
	clock.read = func(context.Context) ([]byte, error) { return nil, errors.New("no date") }
	got, err = clock.now(ctx, now.Add(time.Minute+2*time.Second))
	if err == nil || !got.IsZero() {
		t.Fatal("clock failure silently used UTC")
	}
	clock.read = func(context.Context) ([]byte, error) { return []byte("invalid"), nil }
	if _, err = clock.now(ctx, now.Add(2*time.Minute)); err == nil {
		t.Fatal("invalid clock accepted")
	}
}
