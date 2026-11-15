package daemon

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/Ati054/xroute-openwrt/internal/agentport"
	"github.com/Ati054/xroute-openwrt/internal/settings"
)

// Normalize only settings owned by the live control loop. Source identity,
// listeners, routing rules and the startup graph must still match exactly.
// Independent live edits may arrive together in LuCI's single Apply request.
func liveReloadBase(previous, next settings.Config) (settings.Config, bool) {
	if previous.ProviderID != next.ProviderID || len(previous.Subscriptions) != len(next.Subscriptions) {
		return previous, false
	}
	for i := range previous.Subscriptions {
		if previous.Subscriptions[i].ID != next.Subscriptions[i].ID {
			return previous, false
		}
	}
	previous.Subscriptions = next.Subscriptions
	previous.SubscriptionURL = next.SubscriptionURL
	previous.UpdateMode, previous.UpdateSeconds, previous.MaxNodes = next.UpdateMode, next.UpdateSeconds, next.MaxNodes
	previous.Capture = next.Capture
	return previous, poolOnly(previous, next)
}

// Called with c.mu held. Validate and prepare the pool before changing capture;
// keep the old acknowledged config until both publications have succeeded.
func (c *subscriptionControl) applyLiveReloadLocked(ctx context.Context, path string, previous, next settings.Config, capture func(context.Context, settings.Config) error) error {
	base, allowed := liveReloadBase(previous, next)
	if !allowed {
		return ErrRestartRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	poolChanged := !reflect.DeepEqual(previous.Policies, next.Policies)
	if reflect.DeepEqual(previous.Capture, next.Capture) {
		if !poolChanged {
			return nil
		}
		return c.reloadPoolLocked(ctx, path, base, next)
	}
	var prepared *agentport.PreparedPool
	if poolChanged {
		var err error
		prepared, err = agentport.PreparePool(c.poolNodes, next.Policies)
		if err != nil {
			return err
		}
	}
	return commitLiveCapture(ctx, previous, next, capture, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if prepared == nil {
			return nil
		}
		return prepared.Publish(path, next.Policies)
	})
}

func commitLiveCapture(ctx context.Context, previous, next settings.Config, capture func(context.Context, settings.Config) error, publish func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := capture(ctx, next); err != nil {
		return err
	}
	if err := publish(); err != nil {
		// A cancelled caller must not prevent recovery of its already changed
		// capture rules. Restoration is local, atomic and separately bounded.
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return errors.Join(err, capture(recovery, previous))
	}
	return nil
}
