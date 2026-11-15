package daemon

import (
	"context"
	"errors"
)

// Serialize interface recovery with Apply. A failed/unacknowledged UCI edit
// must not be reintroduced by a later netifd event or periodic VPN check.
func (c *subscriptionControl) restoreAcknowledgedCapture(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.restoreCapture == nil {
		return errors.New("capture recovery is not ready")
	}
	return c.restoreCapture(ctx, c.config)
}
