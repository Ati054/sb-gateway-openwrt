package daemon

import (
	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

// Render the immutable catalog outside the control lock. Overlay current
// monitoring settings only during publication, preserving their control epoch.
func (c *subscriptionControl) publishPool(path string, nodes []catalog.Node) error {
	snapshot := c.snapshot()
	prepared, err := c.preparePool(nodes, snapshot.Policies)
	if err != nil {
		return err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return prepared.Publish(path, c.config.Policies)
}
