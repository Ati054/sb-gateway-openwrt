package agentport

import (
	"context"
	"errors"

	"example.org/resilient-proxy/internal/settings"
)

// UpdatePoolHealth edits the latest published inventory, including a catalog
// awaiting its durable generation commit. It never reconstructs an older pool
// from Store.Current while a subscription transaction is publishing a new one.
func UpdatePoolHealth(ctx context.Context, path string, policies []settings.Policy) error {
	poolPublicationMu.Lock()
	defer poolPublicationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	var pool healthPool
	if err := readJSON(path, &pool); err != nil {
		return err
	}
	if pool.Version < 3 || len(pool.HealthPolicies) != len(policies) {
		return errors.New("health pool does not match configured policies")
	}
	for _, policy := range policies {
		tag := "resproxy-" + policy.ID
		contract, ok := pool.HealthPolicies[tag]
		if !ok || contract.Mode != policy.Mode {
			return errors.New("health pool policy does not match configuration")
		}
		contract.Policy = policy.Health
		pool.HealthPolicies[tag] = contract
	}
	return writeJSONAtomicContext(ctx, path, pool)
}
