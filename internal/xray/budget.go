package xray

import "time"

// RoutingTimeout gives large CIDR tables a bounded compilation window. Small
// rules retain the ordinary deadline. This is an upper budget, not a delay.
func RoutingTimeout(config BaseConfig) time.Duration {
	count := 0
	for _, policy := range config.Policies {
		for _, group := range policy.Rules {
			count += len(group.CIDRs) + len(group.CountryCIDRs)
		}
	}
	if count > 4096 {
		return 330 * time.Second
	}
	return 30 * time.Second
}

func StartupTimeout(config BaseConfig) time.Duration {
	if RoutingTimeout(config) > 30*time.Second {
		return 330 * time.Second
	}
	return 20 * time.Second
}
