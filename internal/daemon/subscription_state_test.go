package daemon

import (
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func TestChangedSubscriptionURLDropsOldValidatorOnlyForThatSource(t *testing.T) {
	first := settings.Subscription{ID: "first", URL: "https://old.example.test/feed", FetchVia: "proxy-first", UpdateMode: "automatic", UpdateSeconds: 1800}
	second := settings.Subscription{ID: "second", URL: "https://other.example.test/feed", UpdateMode: "automatic", UpdateSeconds: 1800}
	state := newSubscriptionRefreshState([]settings.Subscription{first, second})
	now := time.Unix(1000, 0)
	state.nextRefresh["first"], state.nextRefresh["second"] = now.Add(time.Hour), now.Add(time.Hour)
	state.etags["first"], state.etags["second"] = "old-resource-tag", "other-resource-tag"
	state.probeCursors["first"] = 20
	first.URL = "https://new.example.test/feed"
	state.sync([]settings.Subscription{first, second}, now)
	if state.etags["first"] != "" || state.probeCursors["first"] != 0 || !state.nextRefresh["first"].Equal(now) {
		t.Fatal("new URL inherited the old HTTP validator or probe/schedule state")
	}
	if state.etags["second"] != "other-resource-tag" || !state.nextRefresh["second"].Equal(now.Add(time.Hour)) {
		t.Fatal("unrelated source state was reset")
	}
}

func TestSubscriptionScheduleCanSwitchToDailyWithoutRestart(t *testing.T) {
	source := settings.Subscription{ID: "first", URL: "https://example.test/feed", UpdateMode: "automatic", UpdateSeconds: 1800}
	state := newSubscriptionRefreshState([]settings.Subscription{source})
	now := time.Date(2026, 1, 2, 2, 59, 0, 0, time.UTC)
	state.daily[source.ID] = "2026-01-02"
	source.UpdateMode, source.UpdateTime = "daily", "03:00"
	if !state.sync([]settings.Subscription{source}, now) || state.daily.due(source, now) || !state.daily.due(source, now.Add(time.Minute)) {
		t.Fatal("new daily schedule did not activate in the running worker")
	}
	if now.Add(time.Minute).Before(state.nextRefresh[source.ID]) {
		t.Fatal("the previous interval still postpones the new daily update time")
	}
}

func TestChangedDailyUpdateTimeDoesNotWaitForPreviousInterval(t *testing.T) {
	source := settings.Subscription{ID: "first", URL: "https://example.test/feed", UpdateMode: "daily", UpdateTime: "02:00", UpdateSeconds: 21600}
	state := newSubscriptionRefreshState([]settings.Subscription{source})
	now := time.Date(2026, 1, 2, 2, 59, 0, 0, time.UTC)
	state.nextRefresh[source.ID] = now.Add(6 * time.Hour)
	state.daily[source.ID] = "2026-01-02"
	source.UpdateTime = "03:00"
	state.sync([]settings.Subscription{source}, now)
	if now.Add(time.Minute).Before(state.nextRefresh[source.ID]) || !state.daily.due(source, now.Add(time.Minute)) {
		t.Fatal("edited daily schedule is still blocked by previous scheduling state")
	}
}

func TestChangedSubscriptionLimitRequiresCompleteBody(t *testing.T) {
	source := settings.Subscription{ID: "first", URL: "https://example.test/feed", MaxNodes: 100}
	state := newSubscriptionRefreshState([]settings.Subscription{source})
	state.etags[source.ID] = "previous-body"
	source.MaxNodes = 0
	now := time.Unix(1000, 0)
	state.sync([]settings.Subscription{source}, now)
	if state.etags[source.ID] != "" || !state.nextRefresh[source.ID].Equal(now) {
		t.Fatal("changed import limit could accept 304 without applying the new limit")
	}
}
