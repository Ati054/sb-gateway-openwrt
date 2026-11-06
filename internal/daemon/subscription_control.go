package daemon

import (
	"context"
	"log"
	"reflect"
	"sync"

	"github.com/Ati054/sb-gateway-openwrt/internal/agentport"
	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

// Control acknowledgements must stay responsive while HTTPS/probes are busy.
// Monitoring/capture publications are serialized with catalog pool publication;
// fetching and network probes never hold the control lock.
type subscriptionControl struct {
	mu          sync.RWMutex
	config      settings.Config
	refresh     chan string
	readConfig  func(context.Context) (settings.Config, error)
	applyReload func(context.Context, settings.Config, settings.Config) error
	preparePool func([]catalog.Node, []settings.Policy) (*agentport.PreparedPool, error)
}

func newSubscriptionControl(config settings.Config) *subscriptionControl {
	return &subscriptionControl{config: config, refresh: make(chan string, 16), readConfig: settings.ReadSystemUCI, preparePool: agentport.PreparePool}
}

func (c *subscriptionControl) update(config settings.Config) {
	c.mu.Lock()
	c.config = config
	c.mu.Unlock()
}

func (c *subscriptionControl) handle(request reloadRequest) (reloadResult, bool) {
	if request.ctx.Err() != nil {
		return reloadResult{Status: "error", Error: "request expired"}, true
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
	candidate, err := c.readConfig(request.ctx)
	if err != nil {
		return reloadResult{Status: "error", Error: err.Error()}, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if subscriptionSettingsOnly(c.config, candidate) {
		if request.ctx.Err() != nil {
			return reloadResult{Status: "error", Error: "request expired"}, true
		}
		c.config = candidate
		log.Printf("resproxy: subscription settings applied without restarting Xray")
		return reloadResult{Status: "applied"}, true
	}
	result, handled := classifyControlReload(c.config, candidate)
	if handled || c.applyReload == nil {
		return result, handled
	}
	if err := request.ctx.Err(); err != nil {
		return reloadResult{Status: "error", Error: err.Error()}, true
	}
	if err := c.applyReload(request.ctx, c.config, candidate); err != nil {
		return reloadResult{Status: "error", Error: err.Error()}, true
	}
	c.config = candidate
	log.Printf("resproxy: configuration applied without restarting Xray")
	return reloadResult{Status: "applied"}, true
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
	if !monitoringOnly(current, candidate) && !captureOnly(current, candidate) {
		return reloadResult{Status: "restart"}, true
	}
	return reloadResult{}, false
}

func (c *subscriptionControl) snapshot() settings.Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config
}
