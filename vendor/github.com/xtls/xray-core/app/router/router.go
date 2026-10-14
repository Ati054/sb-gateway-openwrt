package router

import (
	"context"
	"maps"
	"sync"
	"sync/atomic"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/routing"
	routing_dns "github.com/xtls/xray-core/features/routing/dns"
)

// Router is an implementation of routing.Router.
type Router struct {
	domainStrategy Config_DomainStrategy
	rules          atomic.Pointer[[]*Rule]
	script         *scriptEngine
	balancers      atomic.Pointer[map[string]*Balancer]
	dns            dns.Client

	ctx        context.Context
	ohm        outbound.Manager
	dispatcher routing.Dispatcher
	mu         sync.Mutex
	overrideMu sync.RWMutex
}

// Route is an implementation of routing.Route.
type Route struct {
	routing.Context
	outboundGroupTags []string
	outboundTag       string
	ruleTag           string
}

// Init initializes the Router.
func (r *Router) Init(ctx context.Context, config *Config, d dns.Client, ohm outbound.Manager, dispatcher routing.Dispatcher) error {
	r.domainStrategy = config.DomainStrategy
	r.dns = d
	r.ctx = ctx
	r.ohm = ohm
	r.dispatcher = dispatcher

	if config.Script != "" {
		script, err := newScriptEngine(config.Script)
		if err != nil {
			return errors.New("failed to prepare routing script").Base(err)
		}
		r.script = script
	}

	r.rules.Store(new([]*Rule))
	r.balancers.Store(&map[string]*Balancer{})
	return r.ReloadRules(config, false)
}

// PickRoute implements routing.Router.
func (r *Router) PickRoute(ctx routing.Context) (routing.Route, error) {
	if r.script != nil {
		return r.script.pickRoute(ctx)
	}

	originalCtx := ctx
	rule, ctx, err := r.pickRouteInternal(ctx)
	if err != nil {
		return nil, err
	}
	tag, err := rule.GetTag()
	if err != nil {
		return nil, err
	}
	if rule.Webhook != nil {
		rule.Webhook.Fire(originalCtx, tag)
	}
	return &Route{Context: ctx, outboundTag: tag, ruleTag: rule.RuleTag}, nil
}

// AddRule implements routing.Router.
func (r *Router) AddRule(config *serial.TypedMessage, shouldAppend bool) error {
	return r.AddRuleContext(context.Background(), config, shouldAppend)
}

// AddRuleContext prevents an expired API request from publishing late routing.
func (r *Router) AddRuleContext(ctx context.Context, config *serial.TypedMessage, shouldAppend bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inst, err := config.GetInstance()
	if err != nil {
		return err
	}
	if c, ok := inst.(*Config); ok {
		return r.ReloadRulesContext(ctx, c, shouldAppend)
	}
	return errors.New("AddRule: config type error")
}

func (r *Router) ReloadRules(config *Config, shouldAppend bool) error {
	return r.ReloadRulesContext(context.Background(), config, shouldAppend)
}

func (r *Router) ReloadRulesContext(ctx context.Context, config *Config, shouldAppend bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	oldRules := *r.rules.Load()
	oldBalancers := *r.balancers.Load()

	var newRules []*Rule
	newBalancers := make(map[string]*Balancer)
	existTags := make(map[string]bool, len(oldRules)+len(config.Rule))
	if shouldAppend {
		newRules = append(newRules, oldRules...)
		maps.Copy(newBalancers, oldBalancers)
		for _, rule := range oldRules {
			existTags[rule.RuleTag] = true
		}
	}

	for _, rule := range config.BalancingRule {
		if _, found := newBalancers[rule.Tag]; found {
			return errors.New("duplicate balancer tag")
		}
		balancer, err := rule.Build(r.ohm, r.dispatcher)
		if err != nil {
			return err
		}
		balancer.InjectContext(r.ctx)
		newBalancers[rule.Tag] = balancer
	}

	published := false
	cleanupStart := 0
	if shouldAppend {
		cleanupStart = len(newRules)
	}
	defer func() {
		if !published {
			closeWebhooks(newRules[cleanupStart:])
		}
	}()
	for _, rule := range config.Rule {
		if err := ctx.Err(); err != nil {
			return err
		}
		cond, err := rule.BuildCondition()
		if err != nil {
			return err
		}
		rr := &Rule{
			Condition: cond,
			Tag:       rule.GetTag(),
			RuleTag:   rule.GetRuleTag(),
		}
		if rr.RuleTag != "" && existTags[rr.RuleTag] {
			return errors.New("duplicate ruleTag ", rr.RuleTag)
		}
		existTags[rr.RuleTag] = true
		if wh := rule.GetWebhook(); wh != nil {
			notifier, err := NewWebhookNotifier(wh)
			if err != nil {
				return err
			}
			rr.Webhook = notifier
		}
		if btag := rule.GetBalancingTag(); len(btag) > 0 {
			brule, found := newBalancers[btag]
			if !found {
				return errors.New("balancer ", btag, " not found")
			}
			rr.Balancer = brule
		}
		newRules = append(newRules, rr)
	}

	// Compile against the immutable old routing while selector API stays live.
	// Carry the latest override only at publication, including changes made
	// while expensive GeoIP conditions were being built.
	r.overrideMu.Lock()
	if err := ctx.Err(); err != nil {
		r.overrideMu.Unlock()
		return err
	}
	for tag, balancer := range newBalancers {
		if previous, found := oldBalancers[tag]; found {
			balancer.override.Put(previous.override.Get())
		}
	}
	r.balancers.Store(&newBalancers)
	r.rules.Store(&newRules)
	published = true
	r.overrideMu.Unlock()
	if !shouldAppend {
		closeWebhooks(oldRules)
	}
	return nil
}

// RemoveRule implements routing.Router.
func (r *Router) RemoveRule(tag string) error {
	if tag == "" {
		return errors.New("empty tag name!")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	oldRules := *r.rules.Load()
	newRules := make([]*Rule, 0, len(oldRules))
	var removed []*Rule
	for _, rule := range oldRules {
		if rule.RuleTag != tag {
			newRules = append(newRules, rule)
		} else {
			removed = append(removed, rule)
		}
	}
	r.rules.Store(&newRules)
	closeWebhooks(removed)
	return nil
}

// ListRule implements routing.Router
func (r *Router) ListRule() []routing.Route {
	rules := *r.rules.Load()
	ruleList := make([]routing.Route, 0, len(rules))
	for _, rule := range rules {
		ruleList = append(ruleList, &Route{
			outboundTag: rule.Tag,
			ruleTag:     rule.RuleTag,
		})
	}
	return ruleList
}

func (r *Router) pickRouteInternal(ctx routing.Context) (*Rule, routing.Context, error) {
	// SkipDNSResolve is set from DNS module.
	// the DOH remote server maybe a domain name,
	// this prevents cycle resolving dead loop
	skipDNSResolve := ctx.GetSkipDNSResolve()

	if r.domainStrategy == Config_IpOnDemand && !skipDNSResolve {
		ctx = routing_dns.ContextWithDNSClient(ctx, r.dns)
	}

	rules := *r.rules.Load()

	for _, rule := range rules {
		if rule.Apply(ctx) {
			return rule, ctx, nil
		}
	}

	if r.domainStrategy != Config_IpIfNonMatch || len(ctx.GetTargetDomain()) == 0 || skipDNSResolve {
		return nil, ctx, common.ErrNoClue
	}

	ctx = routing_dns.ContextWithDNSClient(ctx, r.dns)

	// Try applying rules again if we have IPs.
	for _, rule := range rules {
		if rule.Apply(ctx) {
			return rule, ctx, nil
		}
	}

	return nil, ctx, common.ErrNoClue
}

// Start implements common.Runnable.
func (r *Router) Start() error {
	if r.script != nil {
		if err := r.script.start(r); err != nil {
			return errors.New("failed to initialize routing script").Base(err)
		}
	}
	return nil
}

// closeWebhooks closes all webhook notifiers in the given rule set.
func closeWebhooks(rules []*Rule) {
	for _, rule := range rules {
		if rule.Webhook != nil {
			rule.Webhook.Close()
		}
	}
}

// Close implements common.Closable.
func (r *Router) Close() error {
	if r.script != nil {
		r.script.close()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	closeWebhooks(*r.rules.Load())
	return nil
}

// Type implements common.HasType.
func (*Router) Type() interface{} {
	return routing.RouterType()
}

// GetOutboundGroupTags implements routing.Route.
func (r *Route) GetOutboundGroupTags() []string {
	return r.outboundGroupTags
}

// GetOutboundTag implements routing.Route.
func (r *Route) GetOutboundTag() string {
	return r.outboundTag
}

func (r *Route) GetRuleTag() string {
	return r.ruleTag
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		r := new(Router)
		if err := core.RequireFeatures(ctx, func(d dns.Client, ohm outbound.Manager, dispatcher routing.Dispatcher) error {
			return r.Init(ctx, config.(*Config), d, ohm, dispatcher)
		}); err != nil {
			return nil, err
		}
		return r, nil
	}))
}
