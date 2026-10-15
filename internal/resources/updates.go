package resources

import (
	"context"
	"sync/atomic"
)

var updateLease = make(chan struct{}, 1)
var updating atomic.Bool

// Serialize only expensive catalog/ruleset work; health and emergency recovery
// never wait on this lease. Cancellation prevents shutdown waiting for a turn.
func BeginUpdate(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case updateLease <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	updating.Store(true)
	return func() { updating.Store(false); <-updateLease }, nil
}
func UpdateInProgress() bool { return updating.Load() }
