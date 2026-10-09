package settings

import (
	"context"
	"testing"
)

func TestSourceNamesSupportSeparators(t *testing.T) {
	for _, name := range []string{"vpnd ws", "vpnd-ws", "vpnd_ws", "Vpnd_WS"} {
		config, err := ReadUCI(context.Background(), fake(map[string]string{
			"resproxy.@subscription[0].id":  name,
			"resproxy.@subscription[0].url": "https://example.test/feed",
		}))
		if err != nil || len(config.Subscriptions) != 1 || config.Subscriptions[0].ID != name {
			t.Fatalf("name %q changed/rejected: %v", name, err)
		}
	}
	_, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.@subscription[0].id": "Vpnd_WS", "resproxy.@subscription[0].url": "https://example.test/one",
		"resproxy.@subscription[1].id": "vpnd_ws", "resproxy.@subscription[1].url": "https://example.test/two",
	}))
	if err == nil {
		t.Fatal("case-insensitive duplicate accepted")
	}
}
