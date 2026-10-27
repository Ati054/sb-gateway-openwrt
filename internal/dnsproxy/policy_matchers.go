package dnsproxy

import (
	"fmt"
	"reflect"
)

// Share only immutable matching data inside one published generation. Client
// assignment, selected exits, cache keys and upstream transports remain owned
// by their original policies. Rebuild on reload, including external assets.
func compilePolicyMatchers(policies []Policy) (map[string]domainMatcher, error) {
	matchers := make(map[string]domainMatcher, len(policies))
	type compiledPolicy struct {
		shape   Policy
		matcher domainMatcher
	}
	shared := make([]compiledPolicy, 0, len(policies))
	for _, policy := range policies {
		shape := policy
		shape.ID, shape.SourceCIDRs = "", nil
		// Keep rule/domain order and every matching and outage option. Do not
		// serialize or copy domain lists just to identify duplicates. With at
		// most eight route policies, direct comparison is cheap; distinct lists
		// usually differ at their first rule or domain.
		var matcher domainMatcher
		var exists bool
		for _, entry := range shared {
			if reflect.DeepEqual(shape, entry.shape) {
				matcher, exists = entry.matcher, true
				break
			}
		}
		if !exists {
			var err error
			matcher, err = compileMatcher(policy)
			if err != nil {
				return nil, fmt.Errorf("policy %s DNS routing: %w", policy.ID, err)
			}
			shared = append(shared, compiledPolicy{shape, matcher})
		}
		matchers[policy.ID] = matcher
	}
	return matchers, nil
}
