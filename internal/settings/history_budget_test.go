package settings

import (
	"context"
	"testing"
)

func TestSharedHistoryRAMBudgetValidationAndFallback(t *testing.T) {
	for _, value := range []string{"", "256", "2048", "16384", "255", "16385", "-1", "text"} {
		values := map[string]string{"resproxy.main.history_limit_kib": value}
		config, err := ReadUCI(context.Background(), fake(values))
		invalid := value == "255" || value == "16385" || value == "-1" || value == "text"
		if invalid {
			if err == nil {
				t.Fatalf("invalid budget accepted: %q", value)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		expected := 2048
		switch value {
		case "256":
			expected = 256
		case "16384":
			expected = 16384
		}
		if config.Policies[0].Health.HistoryLimitKiB != expected {
			t.Fatal("fallback lost shared budget", config)
		}
	}
	values := map[string]string{"resproxy.main.history_limit_kib": "512", "resproxy.@policy[0].id": "a", "resproxy.@policy[1].id": "b", "resproxy.@policy[0].history_limit_kib": "16000"}
	config, err := ReadUCI(context.Background(), fake(values))
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range config.Policies {
		if policy.Health.HistoryLimitKiB != 512 {
			t.Fatal("shared budget differs between lists")
		}
	}
}
