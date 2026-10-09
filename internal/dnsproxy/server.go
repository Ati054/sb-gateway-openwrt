// Package dnsproxy serves LAN DNS without changing the router's system DNS.
// Only packets redirected from the configured LAN bridge reach this listener.
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
)

const maxDNSMessage = 4096

type Policy struct {
	ID                       string
	SourceCIDRs              []string
	Rules                    []Rule
	DirectDomains            []string
	DirectServiceExpressions []string
	RUZoneMode               string
}

type Rule struct {
	Action  string
	Domains []string
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
	generationStarted string
	workers           sync.WaitGroup
	config            Config
	proxy             *http.Client
	policyProxies     map[string]*http.Client
	direct            *http.Client
	directAddress     string
	prefixes          []policyPrefix
	matcherMu         sync.RWMutex
	matchers          map[string]domainMatcher
	stateMu           sync.RWMutex
	allDown           map[string]bool
	stateValid        bool
	sem               chan struct{}
	cacheMu           sync.Mutex
	cache             map[string]cacheEntry
	queries           atomic.Uint64
	proxyUse          atomic.Uint64
	directUse         atomic.Uint64
	fallbackUse       atomic.Uint64
	cacheHits         atomic.Uint64
	localUse          atomic.Uint64
	failures          atomic.Uint64
}

type cacheEntry struct {
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
	ordered  []domainActionRule
	exact    map[string]bool
	suffixes map[string]bool
	keywords []string
	patterns []*regexp.Regexp
}

type domainActionRule struct {
	action  string
	matcher domainMatcher
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
	s := &server{config: config,
		proxy:         newDoHClient(proxyTransport),
		direct:        newDoHClient(directTransport),
		policyProxies: make(map[string]*http.Client),
		sem:           make(chan struct{}, 64),
		cache:         make(map[string]cacheEntry),
		matchers:      make(map[string]domainMatcher),
	}
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
		matcher, err := compileMatcher(policy)
		if err != nil {
			return nil, nil, fmt.Errorf("policy %s DNS routing: %w", policy.ID, err)
		}
		s.matchers[policy.ID] = matcher
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
		MaxIdleConns: 16, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second}
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
			"failures": s.failures.Load(),
		}
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
	matchers := make(map[string]domainMatcher, len(policies))
	for _, policy := range policies {
		matcher, err := compileMatcher(policy)
		if err != nil {
			return fmt.Errorf("policy %s DNS routing: %w", policy.ID, err)
		}
		matchers[policy.ID] = matcher
	}
	s.matcherMu.Lock()
	// Publish under the matcher lock, so evidence cannot span the change.
	if err := s.publishGeneration(policies); err != nil {
		s.matcherMu.Unlock()
		return err
	}
	s.matchers = matchers
	s.cacheMu.Lock()
	s.cache = make(map[string]cacheEntry)
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
	result := domainMatcher{exact: make(map[string]bool), suffixes: make(map[string]bool)}
	for _, rule := range policy.Rules {
		if rule.Action != "direct" && rule.Action != "proxy" {
			return domainMatcher{}, errors.New("invalid domain rule action")
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
		matcher, err := compileMatcher(Policy{DirectServiceExpressions: expressions})
		if err != nil {
			return domainMatcher{}, err
		}
		result.ordered = append(result.ordered, domainActionRule{action: rule.Action, matcher: matcher})
	}
	for _, domain := range policy.DirectDomains {
		result.suffixes[strings.ToLower(domain)] = true
	}
	if policy.RUZoneMode == "ru" || policy.RUZoneMode == "both" {
		result.suffixes["ru"] = true
	}
	if policy.RUZoneMode == "rf" || policy.RUZoneMode == "both" {
		result.suffixes["xn--p1ai"] = true
	}
	for _, expression := range policy.DirectServiceExpressions {
		kind, value, ok := strings.Cut(expression, ":")
		if !ok || value == "" {
			return domainMatcher{}, errors.New("invalid service expression")
		}
		switch kind {
		case "full":
			result.exact[strings.ToLower(value)] = true
		case "domain":
			result.suffixes[strings.ToLower(value)] = true
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
	return result, nil
}

func (m domainMatcher) matches(name string) bool {
	for _, rule := range m.ordered {
		if rule.matcher.matches(name) {
			return rule.action == "direct"
		}
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
	body, err := os.ReadFile(s.config.HealthStatePath)
	if err != nil || len(body) > 2*1024*1024 {
		s.invalidateState()
		return
	}
	var states map[string]struct {
		Selected         string `json:"selected"`
		RuntimeSelected  string `json:"runtime_selected"`
		RuntimeConfirmed bool   `json:"runtime_confirmed"`
	}
	if json.Unmarshal(body, &states) != nil {
		s.invalidateState()
		return
	}
	allDown := make(map[string]bool, len(states))
	for tag, state := range states {
		allDown[tag] = state.Selected == "block" && state.RuntimeSelected == "block" && state.RuntimeConfirmed
	}
	s.stateMu.Lock()
	s.allDown = allDown
	s.stateValid = true
	s.stateMu.Unlock()
}

func (s *server) invalidateState() {
	s.stateMu.Lock()
	s.stateValid = false
	s.stateMu.Unlock()
}

func (s *server) useWAN(source netip.Addr) bool {
	return s.useWANPolicy(s.policyFor(source))
}

func (s *server) policyFor(source netip.Addr) string {
	policy := s.config.DefaultPolicy
	for _, item := range s.prefixes {
		if item.prefix.Contains(source.Unmap()) {
			policy = item.id
			break
		}
	}
	return policy
}

func (s *server) useWANPolicy(policy string) bool {
	if s.config.Mode != "proxy-fallback-wan" {
		return false
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.stateValid && s.allDown["resproxy-"+policy]
}

func (s *server) serveUDP(ctx context.Context, conn net.PacketConn) {
	for {
		buffer := make([]byte, maxDNSMessage)
		n, address, err := conn.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("resproxy: LAN DNS UDP listener: %v", err)
			}
			return
		}
		select {
		case s.sem <- struct{}{}:
			s.workers.Add(1)
			go func(query []byte, address net.Addr) {
				defer s.workers.Done()
				defer func() { <-s.sem }()
				response := s.resolve(ctx, query, addrIP(address))
				_, _ = conn.WriteTo(response, address)
			}(buffer[:n], address)
		default:
			_, _ = conn.WriteTo(servfail(buffer[:n]), address)
		}
	}
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
		select {
		case s.sem <- struct{}{}:
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer func() { <-s.sem; _ = conn.Close() }()
				s.handleTCP(ctx, conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (s *server) handleTCP(ctx context.Context, conn net.Conn) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	for i := 0; i < 16; i++ {
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
	s.queries.Add(1)
	name, valid := questionName(query)
	if !valid {
		s.failures.Add(1)
		return servfail(query)
	}
	if isLocalName(name) {
		s.localUse.Add(1)
		response, err := s.askLocal(ctx, query)
		if err == nil {
			return response
		}
		s.failures.Add(1)
		return servfail(query)
	}
	policy := s.policyFor(source)
	routerQuery := source.IsLoopback()
	directRule := routerQuery || s.matchesDirect(policy, name)
	fallback := !directRule && s.useWANPolicy(policy)
	viaWAN := directRule || fallback
	key := policy + ":" + string(query[2:])
	if routerQuery {
		key = "router:" + key
	}
	if viaWAN {
		key = "wan:" + key
	} else {
		key = "proxy:" + key
	}
	if cached := s.cached(key, query[:2]); cached != nil {
		s.cacheHits.Add(1)
		return cached
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
		return servfail(query)
	}
	var body []byte
	if parsed.Protocol == "doh" {
		body, err = exchangeDoH(ctx, client, endpoint, query)
	} else {
		if viaWAN && s.directAddress != "" {
			parsed.Address = s.directAddress
		}
		body, err = exchangeWire(ctx, parsed, query, viaWAN, proxyPort)
	}
	if err != nil || !matchingQuestion(query, body) {
		s.failures.Add(1)
		return servfail(query)
	}
	s.remember(key, body)
	return body
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

func (s *server) cached(key string, transactionID []byte) []byte {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entry, ok := s.cache[key]
	if !ok {
		return nil
	}
	now := time.Now()
	if !now.Before(entry.expires) {
		delete(s.cache, key)
		return nil
	}
	response := append([]byte(nil), entry.response...)
	copy(response[:2], transactionID)
	elapsed := uint32(now.Sub(entry.stored) / time.Second)
	for _, field := range entry.TTLs {
		remaining := uint32(0)
		if field.value > elapsed {
			remaining = field.value - elapsed
		}
		binary.BigEndian.PutUint32(response[field.offset:field.offset+4], remaining)
	}
	return response
}

func (s *server) remember(key string, response []byte) {
	fields, ttl, ok := answerTTLs(response)
	if !ok || ttl == 0 {
		return
	}
	if ttl > 30 {
		ttl = 30
	}
	now := time.Now()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cache == nil {
		s.cache = make(map[string]cacheEntry)
	}
	if len(s.cache) >= 512 {
		for oldKey, old := range s.cache {
			if now.After(old.expires) {
				delete(s.cache, oldKey)
			}
		}
		if len(s.cache) >= 512 {
			for oldKey := range s.cache {
				delete(s.cache, oldKey)
				break
			}
		}
	}
	s.cache[key] = cacheEntry{response: append([]byte(nil), response...), TTLs: fields,
		stored: now, expires: now.Add(time.Duration(ttl) * time.Second)}
}

func answerTTLs(message []byte) ([]ttlField, uint32, bool) {
	if len(message) < 12 || message[3]&0x0f != 0 || binary.BigEndian.Uint16(message[4:6]) != 1 {
		return nil, 0, false
	}
	answers := int(binary.BigEndian.Uint16(message[6:8]))
	if answers == 0 || answers > 128 {
		return nil, 0, false
	}
	offset, ok := skipName(message, 12)
	if !ok || offset+4 > len(message) {
		return nil, 0, false
	}
	offset += 4
	fields := make([]ttlField, 0, answers)
	minimum := uint32(^uint32(0))
	for i := 0; i < answers; i++ {
		offset, ok = skipName(message, offset)
		if !ok || offset+10 > len(message) {
			return nil, 0, false
		}
		ttl := binary.BigEndian.Uint32(message[offset+4 : offset+8])
		if ttl < minimum {
			minimum = ttl
		}
		fields = append(fields, ttlField{offset: offset + 4, value: ttl})
		rdLength := int(binary.BigEndian.Uint16(message[offset+8 : offset+10]))
		offset += 10 + rdLength
		if offset > len(message) {
			return nil, 0, false
		}
	}
	return fields, minimum, true
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
		response[2] |= query[2] & 1
	}
	response[3] = 2
	return response
}
