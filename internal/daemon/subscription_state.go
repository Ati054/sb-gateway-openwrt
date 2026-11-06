package daemon

import (
	"errors"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

var errSubscriptionSettingsChanged = errors.New("subscription settings changed during refresh; previous catalog retained")

type subscriptionRefreshState struct {
	settings     map[string]settings.Subscription
	nextRefresh  map[string]time.Time
	etags        map[string]string
	probeCursors map[string]int
	daily        dailyRefreshSchedule
}

func newSubscriptionRefreshState(sources []settings.Subscription) *subscriptionRefreshState {
	s := &subscriptionRefreshState{settings: map[string]settings.Subscription{}, nextRefresh: map[string]time.Time{}, etags: map[string]string{}, probeCursors: map[string]int{}, daily: dailyRefreshSchedule{}}
	for _, source := range sources {
		s.settings[source.ID] = source
	}
	return s
}

func (s *subscriptionRefreshState) sync(sources []settings.Subscription, now time.Time) bool {
	hasDaily := false
	for _, source := range sources {
		hasDaily = hasDaily || source.UpdateMode == "daily"
		previous, seen := s.settings[source.ID]
		if !seen || previous.URL != source.URL || previous.FetchVia != source.FetchVia || previous.MaxNodes != source.MaxNodes {
			// Conditional validators belong to the downloaded resource, not its
			// source name. A new URL must fetch a complete body before publication.
			delete(s.etags, source.ID)
			delete(s.probeCursors, source.ID)
			s.nextRefresh[source.ID] = now
		} else if previous.UpdateMode != source.UpdateMode || previous.UpdateSeconds != source.UpdateSeconds {
			s.nextRefresh[source.ID] = now.Add(time.Duration(source.UpdateSeconds) * time.Second)
		}
		if !seen || previous.UpdateMode != source.UpdateMode || previous.UpdateTime != source.UpdateTime {
			delete(s.daily, source.ID)
		}
		s.settings[source.ID] = source
	}
	return hasDaily
}
