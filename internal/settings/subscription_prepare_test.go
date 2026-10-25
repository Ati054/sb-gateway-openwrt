package settings

import (
	"context"
	"testing"
)

func TestSubscriptionPreparationIgnoresIncompleteRoutingAndDNS(t *testing.T) {
	values := map[string]string{
		"resproxy.main.enabled": "0", "resproxy.main.route_enabled": "1",
		"resproxy.main.dns_port": "unfinished", "resproxy.main.api_port": "bad",
		"resproxy.@policy[0].id": "", "resproxy.@rule[0].id": "unfinished",
		"resproxy.@subscription[0].id": "first", "resproxy.@subscription[0].url": "https://subscription.example.test/feed",
		"resproxy.@subscription[0].fetch_via": "proxy-first",
	}
	config, err := ReadSubscriptionUCI(context.Background(), fake(values))
	if err != nil || config.Enabled || len(config.Subscriptions) != 1 || config.Subscriptions[0].FetchVia != "proxy-first" {
		t.Fatalf("subscription preparation required a ready proxy: %+v %v", config.Subscriptions, err)
	}
	values["resproxy.@subscription[0].url"] = "http://subscription.example.test/feed"
	if _, err := ReadSubscriptionUCI(context.Background(), fake(values)); err == nil {
		t.Fatal("unsafe subscription URL was accepted")
	}
}
