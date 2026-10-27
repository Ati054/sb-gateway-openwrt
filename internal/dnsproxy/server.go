// Package dnsproxy serves LAN DNS without changing the router's system DNS.
// The daemon installs an input guard before opening the listener: only the
// configured LAN bridge and loopback may reach it, independently of fw4 zones.
package dnsproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
	"github.com/Ati054/sb-gateway-openwrt/internal/healthcontract"
	"github.com/Ati054/sb-gateway-openwrt/internal/resources"
	"github.com/xtls/xray-core/common/geodata"
	"github.com/xtls/xray-core/common/geodata/strmatcher"
)

const maxDNSMessage = 4096

type Policy struct {
	ProxyOutageAction        string
	ID                       string
	SourceCIDRs              []string
	Rules                    []Rule
	DirectDomains            []string
	DirectServiceExpressions []string
	RUZoneMode               string
}

type Rule struct {
	Action       string
	OutageAction string
	Domains      []string
}

type Config struct {
	Port              int
	ProxyPort         int
	PolicyProxyPorts  map[string]int // one local Xray HTTP inbound per route list
	ProxyURL          string
	DirectURL         string
	DirectBootstrapIP string // resolved before taking over router DNS
	Mode              string // proxy-only or proxy-fallback-wan
	DefaultPolicy     string
	Policies          []Policy
	HealthStatePath   string
	GenerationPath    string
	StatusPath        string
}

type server struct {
	generationStarted     string
	workers               sync.WaitGroup
	config                Config
	proxy                 *http.Client
	policyProxies         map[string]*http.Client
	direct                *http.Client
	directAddress         string
	prefixes              []policyPrefix
	matcherMu             sync.RWMutex
	matchers              map[string]domainMatcher
	stateMu               sync.RWMutex
	ruleWANActive         map[string]bool
	proxySelections       map[string]string
	stateValid            bool
	stateFileInfo         os.FileInfo
	stateLease            time.Time
	requestsOnce          sync.Once
	dnsRequests           *dnsRequests
	clientMemory          *memoryGate
	requestBudget         int64
	clientBudget          int64
	udpBufferBytes        int
	cacheMu               sync.Mutex
	cache                 map[string]*cacheEntry
	cacheHead             *cacheEntry
	cacheTail             *cacheEntry
	cacheBytes            int64
	cacheBudget           int64
	cacheEvictions        uint64
	activeQueries         atomic.Int64
	tcpClients            atomic.Int64
	queries               atomic.Uint64
	proxyUse              atomic.Uint64
	directUse             atomic.Uint64
	fallbackUse           atomic.Uint64
	cacheHits             atomic.Uint64
	localUse              atomic.Uint64
	failures              atomic.Uint64
	upstreamTimeouts      atomic.Uint64
	upstreamTransport     atomic.Uint64
	upstreamInvalid       atomic.Uint64
	upstreamHTTPStatus    atomic.Int64
	upstreamExchanges     atomic.Uint64
	upstreamTimeoutPhases [5]atomic.Uint64
}

type cacheEntry struct {
	key      string
	prev     *cacheEntry
	next     *cacheEntry
	bytes    int64
	response []byte
	TTLs     []ttlField
	stored   time.Time
	expires  time.Time
}

type ttlField struct {
	offset int
	value  uint32
}

type policyPrefix struct {
	id     string
	prefix netip.Prefix
}

type domainMatcher struct {
	assets   []geodata.DomainMatcher
	compact  *strmatcher.MphMatcherGroup
	proxyWAN bool
	ordered  []domainActionRule
	exact    map[string]bool
	suffixes map[string]bool
	keywords []string
	patterns []*regexp.Regexp
}

type domainActionRule struct {
	action       string
	outageAction string
	matcher      domainMatcher
}

// Start binds UDP and TCP before returning, so LAN capture cannot be enabled
// with a missing DNS listener. Closing the returned function releases both.
func Start(ctx context.Context, config Config) (func(), func([]Policy) error, error) {
	if config.Port < 1024 || config.Port > 65535 || config.ProxyPort < 1024 || config.ProxyPort > 65535 ||
		config.DefaultPolicy == "" || (config.Mode != "proxy-only" && config.Mode != "proxy-fallback-wan") {
		return nil, nil, errors.New("invalid DNS listener configuration")
	}
	if _, err := ParseEndpoint(config.ProxyURL, false); err != nil {
		return nil, nil, err
	}
	if _, err := ParseEndpoint(config.DirectURL, true); err != nil {
		return nil, nil, err
	}
	directEndpoint, _ := ParseEndpoint(config.DirectURL, true)
	if config.DirectBootstrapIP != "" && net.ParseIP(config.DirectBootstrapIP) == nil {
		return nil, nil, errors.New("invalid direct DNS bootstrap IP")
	}
	matchers, err := compilePolicyMatchers(config.Policies)
	if err != nil {
		return nil, nil, err
	}
	proxyTransport := proxyHTTPTransport(config.ProxyPort)
	directTransport := &http.Transport{ForceAttemptHTTP2: true, MaxIdleConns: 8,
		MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}
	if config.DirectBootstrapIP != "" {
		_, port, _ := net.SplitHostPort(directEndpoint.Address)
		address := net.JoinHostPort(config.DirectBootstrapIP, port)
		directTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}
	}
	totalRAM := resources.CurrentBudget().Total
	s := &server{config: config,
		proxy:         newDoHClient(proxyTransport),
		direct:        newDoHClient(directTransport),
		policyProxies: make(map[string]*http.Client),
		cache:         make(map[string]*cacheEntry),
		cacheBudget:   dnsCacheBudget(totalRAM),
		requestBudget: dnsWorkBudget(totalRAM),
		clientBudget:  dnsWorkBudget(totalRAM),
		matchers:      matchers,
	}
	s.clientMemory = newMemoryGate(s.clientBudget, dnsClientMemory)
	if config.DirectBootstrapIP != "" {
		_, port, _ := net.SplitHostPort(directEndpoint.Address)
		s.directAddress = net.JoinHostPort(config.DirectBootstrapIP, port)
	}
	for _, policy := range config.Policies {
		if port := config.PolicyProxyPorts[policy.ID]; port != 0 {
			if port < 1024 || port > 65535 {
				return nil, nil, fmt.Errorf("invalid DNS proxy port for %s", policy.ID)
			}
			transport := proxyHTTPTransport(port)
			s.policyProxies[policy.ID] = newDoHClient(transport)
		}
		for _, raw := range policy.SourceCIDRs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return nil, nil, err
			}
			s.prefixes = append(s.prefixes, policyPrefix{policy.ID, prefix})
		}
	}
	address := net.JoinHostPort("", fmt.Sprint(config.Port))
	udp, err := net.ListenPacket("udp", address)
	if err != nil {
		return nil, nil, err
	}
	s.udpBufferBytes = configureUDPQueue(udp, s.requestBudget)
	tcp, err := net.Listen("tcp", address)
	if err != nil {
		_ = udp.Close()
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	var once sync.Once
	// Listener loops count as workers before Wait begins; their accepted jobs
	// cannot race WaitGroup.Add against an empty counter during shutdown.
	s.workers.Add(2)
	go func() { defer s.workers.Done(); s.serveUDP(ctx, udp) }()
	go func() { defer s.workers.Done(); s.serveTCP(ctx, tcp) }()
	s.workers.Add(1)
	go func() { defer s.workers.Done(); s.watchState(ctx) }()
	if config.StatusPath != "" {
		s.workers.Add(1)
		go func() { defer s.workers.Done(); s.writeStatus(ctx) }()
	}
	closeAll := func() {
		once.Do(func() {
			cancel()
			_ = udp.Close()
			_ = tcp.Close()
			s.requests().close()
			s.workers.Wait()
			s.proxy.CloseIdleConnections()
			s.direct.CloseIdleConnections()
			for _, client := range s.policyProxies {
				client.CloseIdleConnections()
			}
		})
	}
	if err := s.publishGeneration(config.Policies); err != nil {
		closeAll()
		return nil, nil, err
	}
	go func() { <-ctx.Done(); closeAll() }()
	return closeAll, s.updatePolicies, nil
}

func proxyHTTPTransport(port int) *http.Transport {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	return &http.Transport{Proxy: http.ProxyURL(proxyURL), ForceAttemptHTTP2: true,
		MaxIdleConns: 16, MaxIdleConnsPerHost: 8, // Keep the idle tunnel through a 30-second cache refresh. A selected-node
		// change retires it, so reuse never pins DNS to a previous route.
		IdleConnTimeout: 90 * time.Second}
}

func (s *server) writeStatus(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	defer os.Remove(s.config.StatusPath)
	for {
		status := map[string]uint64{
			"queries": s.queries.Load(), "proxy": s.proxyUse.Load(),
			"direct": s.directUse.Load(), "fallback": s.fallbackUse.Load(),
			"cache_hits": s.cacheHits.Load(), "local": s.localUse.Load(),
			"failures":       s.failures.Load(),
			"active_queries": uint64(s.activeQueries.Load()), "tcp_clients": uint64(s.tcpClients.Load()),
			"request_budget_bytes": uint64(s.requestBudget), "tcp_budget_bytes": uint64(s.clientBudget),
			"upstream_timeouts": s.upstreamTimeouts.Load(), "upstream_transport_errors": s.upstreamTransport.Load(),
			"upstream_invalid_responses": s.upstreamInvalid.Load(), "upstream_last_http_status": uint64(s.upstreamHTTPStatus.Load()),
			"udp_receive_buffer_bytes": uint64(s.udpBufferBytes),
			"upstream_exchanges":       s.upstreamExchanges.Load(),
			"exchange_timeout_connect": s.upstreamTimeoutPhases[0].Load(),
			"exchange_timeout_tls":     s.upstreamTimeoutPhases[1].Load(),
			"exchange_timeout_write":   s.upstreamTimeoutPhases[2].Load(),
			"exchange_timeout_headers": s.upstreamTimeoutPhases[3].Load(),
			"exchange_timeout_body":    s.upstreamTimeoutPhases[4].Load(),
		}
		for key, value := range s.requests().stats() {
			status[key] = value
		}
		s.cacheMu.Lock()
		status["cache_entries"], status["cache_bytes"] = uint64(len(s.cache)), uint64(s.cacheBytes)
		status["cache_budget_bytes"], status["cache_evictions"] = uint64(s.cacheBudget), s.cacheEvictions
		s.cacheMu.Unlock()
		if body, err := json.Marshal(status); err == nil {
			_ = os.WriteFile(s.config.StatusPath, body, 0o600)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *server) updatePolicies(policies []Policy) error {
	matchers, err := compilePolicyMatchers(policies)
	if err != nil {
		return err
	}
	s.matcherMu.Lock()
	// Publish under the matcher lock, so evidence cannot span the change.
	if err := s.publishGeneration(policies); err != nil {
		s.matcherMu.Unlock()
		return err
	}
	s.matchers = matchers
	s.cacheMu.Lock()
	s.clearCacheLocked()
	s.cacheMu.Unlock()
	s.matcherMu.Unlock()
	return nil
}

func (s *server) matchesDirect(policy, name string) bool {
	s.matcherMu.RLock()
	defer s.matcherMu.RUnlock()
	return s.matchers[policy].matches(name)
}

func compileMatcher(policy Policy) (domainMatcher, error) {
	return compileMatcherWithIndex(policy, true)
}

func compileMatcherWithIndex(policy Policy, allowCompact bool) (domainMatcher, error) {
	if policy.ProxyOutageAction != "" && policy.ProxyOutageAction != "block" && policy.ProxyOutageAction != "wan" {
		return domainMatcher{}, errors.New("invalid proxy traffic outage action")
	}
	result := domainMatcher{proxyWAN: policy.ProxyOutageAction == "wan"}
	// Reuse the core's pointer-free arena for large custom lists. Building it
	// directly avoids holding a temporary string map alongside the new index.
	// Small lists keep maps: their savings do not justify building an MPH table.
	domainCount := len(policy.DirectDomains)
	for _, expression := range policy.DirectServiceExpressions {
		if strings.HasPrefix(expression, "full:") || strings.HasPrefix(expression, "domain:") {
			domainCount++
		}
	}
	if allowCompact && domainCount >= compactDomainThreshold {
		result.compact = strmatcher.NewMphMatcherGroup()
	}
	addExact := func(name string) {
		name = strings.ToLower(name)
		if result.compact != nil {
			result.compact.AddFullMatcher(strmatcher.FullMatcher(name), 0)
			return
		}
		if result.exact == nil {
			result.exact = make(map[string]bool)
		}
		result.exact[name] = true
	}
	addSuffix := func(name string) {
		name = strings.ToLower(name)
		// Preserve the legacy map semantics for unusual leading-dot suffixes;
		// Xray's DomainMatcher gives these a different wildcard meaning.
		if result.compact != nil && name != "" && name[0] != '.' {
			result.compact.AddDomainMatcher(strmatcher.DomainMatcher(name), 0)
			return
		}
		if result.suffixes == nil {
			result.suffixes = make(map[string]bool)
		}
		result.suffixes[name] = true
	}
	for _, rule := range policy.Rules {
		if rule.Action != "direct" && rule.Action != "proxy" {
			return domainMatcher{}, errors.New("invalid domain rule action")
		}
		if rule.OutageAction != "" && rule.OutageAction != "block" && rule.OutageAction != "wan" {
			return domainMatcher{}, errors.New("invalid domain rule outage action")
		}
		if len(rule.Domains) == 0 {
			continue
		}
		expressions := make([]string, 0, len(rule.Domains))
		for _, value := range rule.Domains {
			if !strings.Contains(value, ":") {
				value = "domain:" + value
			}
			expressions = append(expressions, value)
		}
		matcher, err := compileMatcherWithIndex(Policy{DirectServiceExpressions: expressions}, allowCompact)
		if err != nil {
			return domainMatcher{}, err
		}
		result.ordered = append(result.ordered, domainActionRule{action: rule.Action, outageAction: rule.OutageAction, matcher: matcher})
	}
	for _, domain := range policy.DirectDomains {
		addSuffix(domain)
	}
	if policy.RUZoneMode == "ru" || policy.RUZoneMode == "both" {
		addSuffix("ru")
	}
	if policy.RUZoneMode == "rf" || policy.RUZoneMode == "both" {
		addSuffix("xn--p1ai")
	}
	for _, expression := range policy.DirectServiceExpressions {
		kind, value, ok := strings.Cut(expression, ":")
		if !ok || value == "" {
			return domainMatcher{}, errors.New("invalid service expression")
		}
		switch kind {
		case "ext":
			root := os.Getenv("XRAY_LOCATION_ASSET")
			if root == "" {
				root = "/etc/resproxy/geoip-assets"
			}
			if err := geoipasset.VerifyDomain(root, expression); err != nil {
				return domainMatcher{}, err
			}
			rules, err := geodata.ParseDomainRules([]string{expression}, geodata.Domain_Domain)
			if err != nil {
				return domainMatcher{}, err
			}
			matcher, err := geodata.DomainReg.BuildDomainMatcher(rules)
			if err != nil {
				return domainMatcher{}, err
			}
			result.assets = append(result.assets, matcher)
		case "full":
			addExact(value)
		case "domain":
			addSuffix(value)
		case "keyword":
			result.keywords = append(result.keywords, strings.ToLower(value))
		case "regexp":
			compiled, err := regexp.Compile("(?i)" + value)
			if err != nil {
				return domainMatcher{}, err
			}
			result.patterns = append(result.patterns, compiled)
		default:
			return domainMatcher{}, errors.New("unknown service expression")
		}
	}
	if result.compact != nil {
		if err := result.compact.Build(); err != nil {
			// MPH has a bounded set of hash multipliers. A collision must not
			// make an otherwise valid custom rule impossible to apply.
			return compileMatcherWithIndex(policy, false)
		}
	}
	return result, nil
}

const compactDomainThreshold = 256

func (m domainMatcher) matches(name string) bool {
	for _, rule := range m.ordered {
		if rule.matcher.matches(name) {
			return rule.action == "direct"
		}
	}
	for _, asset := range m.assets {
		if asset.MatchAny(name) {
			return true
		}
	}
	if m.compact != nil && m.compact.MatchAny(name) {
		return true
	}
	if m.exact[name] {
		return true
	}
	for suffix := name; suffix != ""; {
		if m.suffixes[suffix] {
			return true
		}
		_, next, found := strings.Cut(suffix, ".")
		if !found {
			break
		}
		suffix = next
	}
	for _, keyword := range m.keywords {
		if strings.Contains(name, keyword) {
			return true
		}
	}
	for _, pattern := range m.patterns {
		if pattern.MatchString(name) {
			return true
		}
	}
	return false
}

func (s *server) watchState(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.readState()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *server) readState() {
	info, err := os.Stat(s.config.HealthStatePath)
	if err != nil || time.Since(info.ModTime()) > 20*time.Second {
		s.invalidateState()
		return
	}
	if !s.stateLease.IsZero() && (time.Since(s.stateLease) > 20*time.Second || time.Until(s.stateLease) > time.Second) {
		s.invalidateState()
	}
	// Check the lease on every tick, including cache hits. Atomic replacement
	// with equal size/mtime must still be observed through inode identity.
	if s.stateFileInfo != nil && os.SameFile(info, s.stateFileInfo) && info.ModTime() == s.stateFileInfo.ModTime() && info.Size() == s.stateFileInfo.Size() {
		return
	}
	file, err := os.Open(s.config.HealthStatePath)
	if err != nil {
		s.invalidateState()
		return
	}
	opened, err := file.Stat()
	if err != nil || opened.Size() > 128*1024 {
		file.Close()
		s.invalidateState()
		return
	}
	body, err := io.ReadAll(io.LimitReader(file, 128*1024+1))
	file.Close()
	if err != nil || len(body) > 128*1024 {
		s.invalidateState()
		return
	}
	var snapshot healthcontract.RuntimeSnapshot
	if json.Unmarshal(body, &snapshot) != nil {
		s.invalidateState()
		return
	}
	states := snapshot.Policies
	if states == nil {
		// Compatibility for callers supplying the pre-snapshot runtime shape.
		if json.Unmarshal(body, &states) != nil {
			s.invalidateState()
			return
		}
	} else if snapshot.UpdatedAt.IsZero() || time.Since(snapshot.UpdatedAt) > 20*time.Second || time.Until(snapshot.UpdatedAt) > time.Second {
		s.invalidateState()
		return
	}
	if time.Since(opened.ModTime()) > 20*time.Second {
		s.invalidateState()
		return
	}
	s.stateFileInfo = opened
	s.stateLease = snapshot.UpdatedAt
	ruleWAN := make(map[string]bool, len(states))
	selections := make(map[string]string, len(states))
	for tag, state := range states {
		confirmedOutage := state.Selected == "block" && state.RuntimeSelected == "block" && state.RuntimeConfirmed
		ruleWAN[tag] = confirmedOutage && state.WANFallbackActive
		if state.RuntimeConfirmed && state.RuntimeSelected == state.Selected {
			selections[tag] = state.RuntimeSelected
		}
	}
	var changed []string
	s.stateMu.Lock()
	for tag, state := range states {
		previous := s.proxySelections[tag]
		// A failed selector observation is not a routing change. Keep the
		// existing proxy session and its route-scoped cache while the intended
		// leaf is unchanged. Rebuilding them on an API timeout creates a cold
		// TLS/DNS burst precisely when the router is already overloaded.
		// This never confirms health or authorizes WAN fallback; a changed,
		// blocked or removed route still retires its previous transport.
		if !state.RuntimeConfirmed && previous != "" && previous != "block" && previous != "direct-wan" &&
			state.Selected == previous && (state.RuntimeSelected == "" || state.RuntimeSelected == previous) {
			selections[tag] = previous
		}
	}
	for tag, previous := range s.proxySelections {
		if selections[tag] != previous {
			changed = append(changed, tag)
		}
	}
	s.ruleWANActive, s.proxySelections, s.stateValid = ruleWAN, selections, true
	// Retire before publishing the new selection to DNS jobs. Existing requests
	// drain on their original route and have a separate route-scoped cache key.
	for _, tag := range changed {
		policy := strings.TrimPrefix(tag, "resproxy-")
		client := s.policyProxies[policy]
		if client == nil && policy == s.config.DefaultPolicy {
			client = s.proxy
		}
		if client != nil {
			if owner, ok := client.Transport.(*recoveringTransport); ok {
				owner.mu.Lock()
				previous := owner.current
				owner.mu.Unlock()
				owner.retire(previous)
			}
		}
	}
	s.stateMu.Unlock()
}

func (s *server) invalidateState() {
	s.stateMu.Lock()
	s.stateValid = false
	s.stateFileInfo = nil
	s.stateLease = time.Time{}
	s.stateMu.Unlock()
}

func (s *server) policyFor(source netip.Addr) string {
	policy, _ := s.policyForSource(source)
	return policy
}

func (s *server) policyForSource(source netip.Addr) (string, bool) {
	policy := s.config.DefaultPolicy
	for _, item := range s.prefixes {
		if item.prefix.Contains(source.Unmap()) {
			return item.id, true
		}
	}
	if len(s.config.Policies) == 0 {
		return policy, true // Legacy callers provide one implicit common policy.
	}
	for _, item := range s.config.Policies {
		if len(item.SourceCIDRs) == 0 {
			return policy, true
		}
	}
	return policy, false
}

// DNS honors the first applicable domain rule, including strict proxy rules
// preceding a broader WAN-fallback rule. Stale health cannot authorize WAN.
func (s *server) useRuleWAN(policy, name string) bool {
	s.stateMu.RLock()
	active := s.stateValid && s.ruleWANActive["resproxy-"+policy]
	s.stateMu.RUnlock()
	if !active {
		return false
	}
	s.matcherMu.RLock()
	defer s.matcherMu.RUnlock()
	if s.matchers[policy].proxyWAN {
		return true
	}
	for _, rule := range s.matchers[policy].ordered {
		if rule.matcher.matches(name) {
			return rule.action == "proxy" && rule.outageAction == "wan"
		}
	}
	return false
}

func (s *server) serveUDP(ctx context.Context, conn net.PacketConn) {
	var packet [maxDNSMessage]byte
	for {
		// Backpressure is based on a RAM budget, not a fixed client count.
		// Only this listener waits: no unbounded queue of waiting goroutines.
		n, address, err := conn.ReadFrom(packet[:])
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("resproxy: LAN DNS UDP listener: %v", err)
			}
			return
		}
		// A DoH request body can outlive an early transport error. Give every
		// query independent backing storage instead of recycling its bytes.
		query := append([]byte(nil), packet[:n]...)
		s.resolveAsync(ctx, query, addrIP(address), func(response []byte) {
			_, _ = conn.WriteTo(response, address)
		})
	}
}

const dnsRequestMemory int64 = 8 << 10
const dnsClientMemory int64 = 4 << 10

func dnsWorkBudget(totalRAM int64) int64 { return max(512<<10, totalRAM/64) }

// Retain a short burst in the socket while request workers are scheduled. The
// kernel may clamp this request to its own rmem_max; no global sysctl changes.
// This storage is additional kernel memory, separate from the work estimate.
func configureUDPQueue(conn net.PacketConn, workBudget int64) int {
	if udp, ok := conn.(*net.UDPConn); ok {
		actual, err := setUDPReadBuffer(udp, int(max(128<<10, min(1<<20, workBudget/4))))
		if err != nil {
			log.Printf("resproxy: DNS receive buffer: %v", err)
		}
		return actual
	}
	return 0
}

func (s *server) serveTCP(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("resproxy: LAN DNS TCP listener: %v", err)
			}
			return
		}
		if err := s.clientMemory.acquire(ctx); err != nil {
			_ = conn.Close()
			return
		}
		s.workers.Add(1)
		s.tcpClients.Add(1)
		go func() {
			defer s.workers.Done()
			defer s.tcpClients.Add(-1)
			defer s.clientMemory.release()
			defer conn.Close()
			s.handleTCP(ctx, conn)
		}()
	}
}

func (s *server) handleTCP(ctx context.Context, conn net.Conn) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	for ctx.Err() == nil {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		var length [2]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return
		}
		size := int(binary.BigEndian.Uint16(length[:]))
		if size < 12 || size > maxDNSMessage {
			return
		}
		query := make([]byte, size)
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		response := s.resolve(ctx, query, addrIP(conn.RemoteAddr()))
		binary.BigEndian.PutUint16(length[:], uint16(len(response)))
		packet := append(length[:], response...)
		if _, err := io.Copy(conn, bytes.NewReader(packet)); err != nil {
			return
		}
	}
}

func (s *server) resolve(ctx context.Context, query []byte, source netip.Addr) []byte {
	response := make(chan []byte, 1)
	s.resolveAsync(ctx, query, source, func(body []byte) { response <- body })
	return <-response
}

func (s *server) resolveAsync(ctx context.Context, query []byte, source netip.Addr, done func([]byte)) {
	s.activeQueries.Add(1)
	finish := func(body []byte) { s.activeQueries.Add(-1); done(body) }
	s.queries.Add(1)
	name, valid := questionName(query)
	if !valid {
		s.failures.Add(1)
		finish(servfail(query))
		return
	}
	if isLocalName(name) {
		s.localUse.Add(1)
		err := s.requests().submit(ctx, "local", "local:"+string(query[2:]), query, func(lookupCtx context.Context) ([]byte, error) { return s.askLocal(lookupCtx, query) }, func(body []byte, err error) {
			if err != nil {
				s.failures.Add(1)
			}
			finish(body)
		})
		if err != nil {
			s.failures.Add(1)
			finish(servfail(query))
		}
		return
	}
	policy, assigned := s.policyForSource(source)
	routerQuery := source.IsLoopback()
	directRule := routerQuery || !assigned || s.matchesDirect(policy, name)
	fallback := !directRule && (s.useRuleWAN(policy, name))
	viaWAN := directRule || fallback
	// Authorization happens before cache lookup. Every permitted WAN query
	// uses this server's same immutable resolver and direct transport, so
	// devices and policies can share its cache and pending lookup. Proxy
	// answers stay isolated by policy and selected exit, even at the same URL.
	var key, selected string
	if viaWAN {
		key = "wan:" + string(query[2:])
	} else {
		s.stateMu.RLock()
		selected = s.proxySelections["resproxy-"+policy]
		s.stateMu.RUnlock()
		key = "proxy:" + policy + ":" + selected + ":" + string(query[2:])
	}
	if cached := s.cached(key, query[:2]); cached != nil {
		s.cacheHits.Add(1)
		finish(cached)
		return
	}
	client, endpoint := s.proxy, s.config.ProxyURL
	if specific := s.policyProxies[policy]; specific != nil {
		client = specific
	}
	proxyPort := s.config.ProxyPort
	if specific := s.config.PolicyProxyPorts[policy]; specific != 0 {
		proxyPort = specific
	}
	if viaWAN {
		client, endpoint = s.direct, s.config.DirectURL
		if directRule {
			s.directUse.Add(1)
		} else {
			s.fallbackUse.Add(1)
		}
	} else {
		s.proxyUse.Add(1)
	}
	parsed, err := ParseEndpoint(endpoint, viaWAN)
	if err != nil {
		s.failures.Add(1)
		finish(servfail(query))
		return
	}
	// Listener jobs share the service context and upstream's five-second
	// deadline. Collapse a cold burst before opening duplicate TLS connections.
	// The key includes the route and complete query except ID; proxy routes
	// additionally include their policy and selected exit.
	path := "wan"
	if !viaWAN {
		path = "proxy:" + policy
	}
	err = s.requests().submit(ctx, path, key, query, func(lookupCtx context.Context) ([]byte, error) {
		if cached := s.cached(key, query[:2]); cached != nil {
			return cached, nil
		}
		var body []byte
		var err error
		s.upstreamExchanges.Add(1)
		if parsed.Protocol == "doh" {
			body, err = exchangeDoH(lookupCtx, client, endpoint, query)
		} else {
			if viaWAN && s.directAddress != "" {
				parsed.Address = s.directAddress
			}
			body, err = exchangeWire(lookupCtx, parsed, query, viaWAN, proxyPort)
		}
		if err != nil {
			var timeout *upstreamTimeoutPhase
			if errors.As(err, &timeout) && timeout.phase >= 0 && timeout.phase < len(s.upstreamTimeoutPhases) {
				s.upstreamTimeoutPhases[timeout.phase].Add(1)
			}
			return nil, err
		}
		if !matchingQuestion(query, body) {
			return nil, errors.New("DNS response does not match question")
		}
		current := true
		if !viaWAN {
			s.stateMu.RLock()
			current = s.proxySelections["resproxy-"+policy] == selected
			s.stateMu.RUnlock()
		}
		if current && lookupCtx.Err() == nil {
			s.remember(key, body)
		}
		return body, nil
	}, func(body []byte, err error) {
		if err != nil {
			s.failures.Add(1)
			if errors.Is(err, errDNSQueued) {
				finish(body)
				return
			}
			var responseErr upstreamResponseError
			var networkErr net.Error
			switch {
			case errors.As(err, &responseErr):
				s.upstreamInvalid.Add(1)
				s.upstreamHTTPStatus.Store(int64(responseErr.status))
			case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkErr) && networkErr.Timeout()):
				s.upstreamTimeouts.Add(1)
			default:
				s.upstreamTransport.Add(1)
			}
		}
		finish(body)
	})
	if err != nil {
		s.failures.Add(1)
		finish(servfail(query))
	}
}

func exchangeWire(ctx context.Context, endpoint Endpoint, query []byte, direct bool, proxyPort int) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var conn net.Conn
	var err error
	if direct {
		network := "tcp"
		if endpoint.Protocol == "udp" {
			network = "udp"
		}
		conn, err = (&net.Dialer{}).DialContext(requestCtx, network, endpoint.Address)
	} else {
		conn, err = dialProxyConnect(requestCtx, proxyPort, endpoint.Address)
	}
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	rawConn := conn
	stop := context.AfterFunc(requestCtx, func() { _ = rawConn.Close() })
	defer stop()
	deadline, ok := requestCtx.Deadline()
	if ok {
		_ = conn.SetDeadline(deadline)
	}
	if endpoint.Protocol == "dot" {
		secured := tls.Client(conn, &tls.Config{ServerName: endpoint.ServerName, MinVersion: tls.VersionTLS12})
		if err := secured.HandshakeContext(requestCtx); err != nil {
			return nil, err
		}
		conn = secured
	}
	if endpoint.Protocol == "udp" {
		if _, err := conn.Write(query); err != nil {
			return nil, err
		}
		buffer := make([]byte, maxDNSMessage+1)
		n, err := conn.Read(buffer)
		if err != nil {
			return nil, err
		}
		if n >= 3 && buffer[2]&0x02 != 0 {
			fallback := endpoint
			fallback.Protocol = "tcp"
			return exchangeWire(requestCtx, fallback, query, true, proxyPort)
		}
		return buffer[:n], nil
	}
	length := []byte{byte(len(query) >> 8), byte(len(query))}
	if _, err := io.Copy(conn, bytes.NewReader(append(length, query...))); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(conn, length); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(length))
	if size < 12 || size > maxDNSMessage {
		return nil, errors.New("invalid DNS response length")
	}
	response := make([]byte, size)
	_, err = io.ReadFull(conn, response)
	return response, err
}

func dialProxyConnect(ctx context.Context, port int, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", address, address); err != nil {
		conn.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("Xray DNS CONNECT returned %d", response.StatusCode)
	}
	return conn, nil
}

func answerTTLs(message []byte) ([]ttlField, uint32, bool) {
	if len(message) < 12 || message[3]&0x0f != 0 || binary.BigEndian.Uint16(message[4:6]) != 1 {
		return nil, 0, false
	}
	answers := int(binary.BigEndian.Uint16(message[6:8]))
	if answers == 0 || answers > 128 {
		return nil, 0, false
	}
	records := answers + int(binary.BigEndian.Uint16(message[8:10])) + int(binary.BigEndian.Uint16(message[10:12]))
	if records > 384 {
		return nil, 0, false
	}
	offset, ok := skipName(message, 12)
	if !ok || offset+4 > len(message) {
		return nil, 0, false
	}
	offset += 4
	fields := make([]ttlField, 0, records)
	minimum := uint32(^uint32(0))
	for i := 0; i < records; i++ {
		offset, ok = skipName(message, offset)
		if !ok || offset+10 > len(message) {
			return nil, 0, false
		}
		// OPT's TTL word contains EDNS version/flags, not a cache lifetime.
		// Every other section ages with the answer and can shorten its lifetime.
		if binary.BigEndian.Uint16(message[offset:offset+2]) != 41 {
			ttl := binary.BigEndian.Uint32(message[offset+4 : offset+8])
			if ttl < minimum {
				minimum = ttl
			}
			fields = append(fields, ttlField{offset: offset + 4, value: ttl})
		}
		rdLength := int(binary.BigEndian.Uint16(message[offset+8 : offset+10]))
		offset += 10 + rdLength
		if offset > len(message) {
			return nil, 0, false
		}
	}
	return fields, minimum, len(fields) != 0
}

func skipName(message []byte, offset int) (int, bool) {
	for steps := 0; steps < 128 && offset < len(message); steps++ {
		length := int(message[offset])
		offset++
		if length == 0 {
			return offset, true
		}
		if length&0xc0 == 0xc0 {
			return offset + 1, offset < len(message)
		}
		if length > 63 || offset+length > len(message) {
			return 0, false
		}
		offset += length
	}
	return 0, false
}

func (s *server) askLocal(ctx context.Context, query []byte) ([]byte, error) {
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "udp", "127.0.0.1:53")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buffer := make([]byte, maxDNSMessage)
	n, err := conn.Read(buffer)
	if err != nil || !matchingQuestion(query, buffer[:n]) {
		return nil, errors.New("invalid local DNS response")
	}
	return buffer[:n], nil
}

func addrIP(address net.Addr) netip.Addr {
	switch value := address.(type) {
	case *net.UDPAddr:
		return value.AddrPort().Addr()
	case *net.TCPAddr:
		return value.AddrPort().Addr()
	default:
		return netip.Addr{}
	}
}

func questionName(query []byte) (string, bool) {
	if len(query) < 17 || query[2]&0x80 != 0 || binary.BigEndian.Uint16(query[4:6]) != 1 {
		return "", false
	}
	labels := []string{}
	for offset := 12; offset < len(query); {
		length := int(query[offset])
		offset++
		if length == 0 {
			return strings.ToLower(strings.Join(labels, ".")), offset+4 <= len(query)
		}
		if length > 63 || offset+length > len(query) || len(labels) >= 127 {
			return "", false
		}
		labels = append(labels, string(query[offset:offset+length]))
		offset += length
	}
	return "", false
}

func isLocalName(name string) bool {
	return name == "lan" || strings.HasSuffix(name, ".lan") ||
		name == "localhost" || strings.HasSuffix(name, ".localhost") ||
		name == "home.arpa" || strings.HasSuffix(name, ".home.arpa")
}

func servfail(query []byte) []byte {
	response := make([]byte, 12)
	if len(query) >= 2 {
		copy(response[:2], query[:2])
	}
	response[2] = 0x80
	if len(query) >= 3 {
		response[2] |= query[2] & 0x79
	}
	response[3] = 2
	// Resolvers match errors to their original question too. A header-only
	// reply to a valid query is rejected as malformed by clients such as c-ares.
	if _, valid := questionName(query); valid {
		if end, ok := skipName(query, 12); ok && end+4 <= len(query) {
			response[5] = 1
			response = append(response, query[12:end+4]...)
		}
	}
	return response
}
