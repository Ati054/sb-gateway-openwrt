package daemon

import (
	"context"

	"github.com/Ati054/xroute-openwrt/internal/agentport"
	"github.com/Ati054/xroute-openwrt/internal/catalog"
	"github.com/Ati054/xroute-openwrt/internal/settings"
)

// Render the immutable catalog outside the control lock. Overlay current
// monitoring settings only during publication, preserving their control epoch.
// If membership changed while rendering, rebase on this same inventory before
// committing. A stale prepared pool must not undo an acknowledged Apply.
func (c *subscriptionControl) publishPool(path string, nodes []catalog.Node) error {
	snapshot := c.snapshot()
	nodes = configuredSourceNodes(nodes, snapshot.Subscriptions)
	prepared, err := c.preparePool(nodes, snapshot.Policies)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !monitoringOnly(settings.Config{Policies: snapshot.Policies}, settings.Config{Policies: c.config.Policies}) {
		prepared, err = c.preparePool(nodes, c.config.Policies)
		if err != nil {
			return err
		}
	}
	if err := prepared.Publish(path, c.config.Policies); err != nil {
		return err
	}
	c.poolNodes = nodes
	return nil
}

// Called with the control lock held, serializing Apply with pool publication.
// A health-only edit keeps the existing cheap overlay path.
func (c *subscriptionControl) reloadPoolLocked(ctx context.Context, path string, previous, next settings.Config) error {
	if monitoringOnly(previous, next) {
		return agentport.UpdatePoolHealth(ctx, path, next.Policies)
	}
	return applyMonitoringReload(ctx, previous, next, c.poolNodes, path)
}
