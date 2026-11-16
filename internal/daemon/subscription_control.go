package daemon

import (
	"context"
	"errors"
	"log"
	"reflect"
	"sync"
	"time"

	"github.com/Ati054/xroute-openwrt/internal/agentport"
	"github.com/Ati054/xroute-openwrt/internal/catalog"
	"github.com/Ati054/xroute-openwrt/internal/dnsproxy"
	"github.com/Ati054/xroute-openwrt/internal/settings"
)

// Control acknowledgements must stay responsive while HTTPS/probes are busy.
// Monitoring/capture publications are serialized with catalog pool publication;
// fetching and network probes never hold the control lock.
type subscriptionControl struct {
	// Serialize UCI reads with their Apply commits, preventing an older slow
	// read from overwriting a newer acknowledged configuration.
	applyMu sync.Mutex
	mu      sync.RWMutex
	config  settings.Config
	// Immutable inventory of the published pool, which may precede the store's
	// durable commit. Never rebuild a live pool from a different generation.
	poolNodes      []catalog.Node
	refresh        chan string
	wanChanges     chan struct{}
	readConfig     func(context.Context) (settings.Config, error)
	applyReload    func(context.Context, settings.Config, settings.Config) error
	restoreCapture func(context.Context, settings.Config) error
	preparePool    func([]catalog.Node, []settings.Policy) (*agentport.PreparedPool, error)
	onApplied      func()
	beforeApply    func(context.Context) error
	persistApplied func(settings.Config) error
	onFatal        func(error)
	recoveryMode   bool
}

func newSubscriptionControl(config settings.Config) *subscriptionControl {
	c := &subscriptionControl{config: config, refresh: make(chan string, 16), readConfig: settings.ReadSystemUCI, preparePool: agentport.PreparePool}
	if config.DNSDirectURL == dnsproxy.WANResolverURL && config.DNSMode != "system" {
		c.wanChanges = make(chan struct{}, 1)
	}
	return c
}

func (c *subscriptionControl) update(config settings.Config) {
	c.mu.Lock()
	c.config = config
	c.mu.Unlock()
	if c.onApplied != nil {
		c.onApplied()
	}
}

func (c *subscriptionControl) handle(request reloadRequest) (reloadResult, bool) {
	if request.ctx.Err() != nil {
		return reloadResult{Status: "error", Error: "request expired"}, true
	}
	if request.operation == "wan-dns-refresh" {
		select {
		case c.wanChanges <- struct{}{}:
		default:
		}
		return reloadResult{Status: "applied"}, true
	}
	if request.operation == "capture-restore" {
		if err := c.restoreAcknowledgedCapture(request.ctx); err != nil {
			return reloadResult{Status: "error", Error: err.Error()}, true
		}
		return reloadResult{Status: "applied"}, true
	}
	c.mu.RLock()
	current := c.config
	c.mu.RUnlock()
	if request.operation == "refresh" {
		result := refreshAcceptance(current.Subscriptions, request.source, request.signature)
		if result.Status == "applied" {
			select {
			case c.refresh <- request.source:
			default:
				result = reloadResult{Status: "error", Error: "subscription request queue is full"}
			}
		}
		return result, true
	}
	if c.beforeApply != nil {
		if err := c.beforeApply(request.ctx); err != nil {
			return reloadResult{Status: "error", Error: err.Error()}, true
		}
	}
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	if err := request.ctx.Err(); err != nil {
		return reloadResult{Status: "error", Error: err.Error()}, true
	}
	candidate, err := c.readConfig(request.ctx)
	if err != nil {
		return reloadResult{Status: "error", Error: err.Error()}, true
	}
	// Recovery runs a pinned, resolved route graph with automatic updates
	// suspended. Only a normal startup can safely resume desired UCI settings
	// and all of their workers, even when the edit looks pool-only.
	if c.recoveryMode {
		return reloadResult{Status: "restart"}, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if subscriptionSettingsOnly(c.config, candidate) {
		if request.ctx.Err() != nil {
			return reloadResult{Status: "error", Error: "request expired"}, true
		}
		if err := c.persistCommit(request.ctx, c.config, candidate, false); err != nil {
			return reloadResult{Status: "error", Error: err.Error()}, true
		}
		c.config = candidate
		if c.onApplied != nil {
			c.onApplied()
		}
		log.Printf("resproxy: subscription settings applied without restarting Xray")
		return reloadResult{Status: "applied"}, true
	}
	result, handled := classifyControlReload(c.config, candidate)
	if handled || c.applyReload == nil {
		if handled && result.Status == "applied" {
			if err := c.persistCommit(request.ctx, c.config, candidate, false); err != nil {
				return reloadResult{Status: "error", Error: err.Error()}, true
			}
		}
		if handled && result.Status == "applied" && c.onApplied != nil {
			c.onApplied()
		}
		return result, handled
	}
	if err := request.ctx.Err(); err != nil {
		return reloadResult{Status: "error", Error: err.Error()}, true
	}
	if err := c.applyReload(request.ctx, c.config, candidate); err != nil {
		var rollback *runtimeRollbackError
		if errors.As(err, &rollback) && c.onFatal != nil {
			c.onFatal(err)
		}
		return reloadResult{Status: "error", Error: err.Error()}, true
	}
	if err := c.persistCommit(request.ctx, c.config, candidate, true); err != nil {
		return reloadResult{Status: "error", Error: err.Error()}, true
	}
	c.config = candidate
	if c.onApplied != nil {
		c.onApplied()
	}
	log.Printf("resproxy: configuration applied without restarting Xray")
	return reloadResult{Status: "applied"}, true
}

func (c *subscriptionControl) persistCommit(ctx context.Context, previous, next settings.Config, runtimeChanged bool) error {
	if c.persistApplied == nil {
		return nil
	}
	if err := c.persistApplied(next); err != nil {
		var rollback error
		if runtimeChanged && c.applyReload != nil {
			recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			rollback = c.applyReload(recovery, next, previous)
			cancel()
		}
		if rollback != nil {
			fatal := errors.Join(err, rollback)
			// The durable snapshot still describes the last acknowledged plan.
			// An unsuccessful runtime rollback must enter normal supervised
			// recovery rather than continue serving an uncertain mixture.
			if c.onFatal != nil {
				c.onFatal(fatal)
			}
			return fatal
		}
		return errors.Join(err, c.persistApplied(previous))
	}
	return nil
}

// Endpoint and scheduling edits do not change the active routing contract.
// Source additions/removals/renames still require the normal catalog lifecycle.
func subscriptionSettingsOnly(previous, next settings.Config) bool {
	if len(previous.Subscriptions) == 0 || reflect.DeepEqual(previous, next) || len(previous.Subscriptions) != len(next.Subscriptions) || previous.ProviderID != next.ProviderID {
		return false
	}
	for i := range previous.Subscriptions {
		if previous.Subscriptions[i].ID != next.Subscriptions[i].ID {
			return false
		}
	}
	previous.Subscriptions = next.Subscriptions
	previous.SubscriptionURL = next.SubscriptionURL
	previous.UpdateMode, previous.UpdateSeconds, previous.MaxNodes = next.UpdateMode, next.UpdateSeconds, next.MaxNodes
	return reflect.DeepEqual(previous, next)
}

func (c *subscriptionControl) sourceCurrent(source settings.Subscription) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, current := range c.config.Subscriptions {
		if current.ID == source.ID {
			return current == source
		}
	}
	return false
}

func classifyControlReload(current, candidate settings.Config) (reloadResult, bool) {
	if reflect.DeepEqual(current, candidate) {
		return reloadResult{Status: "applied"}, true
	}
	if _, allowed := liveReloadBase(current, candidate); !allowed {
		return reloadResult{Status: "restart"}, true
	}
	return reloadResult{}, false
}

func (c *subscriptionControl) snapshot() settings.Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config
}
