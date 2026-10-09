package settings

import (
	"context"
	"testing"
)

func TestSubscriptionDailyTime(t *testing.T) {
	for _, at := range []string{"00:00", "03:15", "23:59"} {
		config, err := ReadUCI(context.Background(), fake(map[string]string{
			"resproxy.@subscription[0].id":          "office",
			"resproxy.@subscription[0].url":         "https://example.test/feed",
			"resproxy.@subscription[0].update_mode": "daily",
			"resproxy.@subscription[0].update_time": at,
		}))
		if err != nil || len(config.Subscriptions) != 1 || config.Subscriptions[0].UpdateTime != at {
			t.Fatalf("daily time %s: %+v, %v", at, config.Subscriptions, err)
		}
	}
	for _, at := range []string{"", "24:00", "12:60", "3:00", "12:00:10", "today"} {
		_, err := ReadUCI(context.Background(), fake(map[string]string{
			"resproxy.@subscription[0].id":          "office",
			"resproxy.@subscription[0].url":         "https://example.test/feed",
			"resproxy.@subscription[0].update_mode": "daily",
			"resproxy.@subscription[0].update_time": at,
		}))
		if err == nil {
			t.Fatalf("invalid daily time accepted: %q", at)
		}
	}
}

func TestSubscriptionInactiveTimeIgnored(t *testing.T) {
	for _, mode := range []string{"automatic", "manual"} {
		_, err := ReadUCI(context.Background(), fake(map[string]string{
			"resproxy.@subscription[0].id":          "office",
			"resproxy.@subscription[0].url":         "https://example.test/feed",
			"resproxy.@subscription[0].update_mode": mode,
			"resproxy.@subscription[0].update_time": "not active",
		}))
		if err != nil {
			t.Fatal(err)
		}
	}
}
