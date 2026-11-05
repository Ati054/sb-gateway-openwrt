package settings

import (
	"context"
	"testing"
)

func TestLegacyNodeCountSettingsDoNotTruncateSubscriptions(t *testing.T) {
	for _, limit := range []string{"1", "1000", "5000"} {
		values := map[string]string{"resproxy.main.provider_id": "provider", "resproxy.main.subscription_url": "https://example.test/feed", "resproxy.main.max_nodes": limit}
		config, err := ReadUCI(context.Background(), fake(values))
		if err != nil || config.MaxNodes != 0 || len(config.Subscriptions) != 1 || config.Subscriptions[0].MaxNodes != 0 {
			t.Fatalf("legacy count cap remained active: %+v %v", config.Subscriptions, err)
		}
	}
}
