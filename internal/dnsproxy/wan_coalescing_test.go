package dnsproxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func wanSharingServer(t *testing.T, client *http.Client, endpoint string) *server {
	t.Helper()
	s := &server{
		config: Config{DefaultPolicy: "main", Mode: "proxy-only", DirectURL: endpoint, ProxyURL: endpoint},
		direct: client, proxy: client,
		prefixes: []policyPrefix{
			{"other", netip.MustParsePrefix("192.0.2.0/24")},
			{"fallback", netip.MustParsePrefix("203.0.113.0/24")},
		},
		matchers: make(map[string]domainMatcher), stateValid: true,
		ruleWANActive: map[string]bool{"resproxy-fallback": true},
	}
	for _, policy := range []Policy{
		{ID: "main", DirectDomains: []string{"example.com"}},
		{ID: "other", DirectDomains: []string{"example.com"}},
		{ID: "fallback", ProxyOutageAction: "wan"},
	} {
		matcher, err := compileMatcher(policy)
		if err != nil {
			t.Fatal(err)
		}
		s.matchers[policy.ID] = matcher
	}
	return s
}

func TestWANColdBurstSharesAcrossDevicesPoliciesAndRouter(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var unblock sync.Once
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query, _ := io.ReadAll(r.Body)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(dohReply(query)) // No TTL: sharing must happen before caching.
	}))
	defer upstream.Close()
	defer unblock.Do(func() { close(release) })
	s := wanSharingServer(t, upstream.Client(), upstream.URL+"/dns-query")
	const clients = 128
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			prefix := []string{"198.51.100", "192.0.2", "203.0.113", "127.0.0"}[i%4]
			source := netip.MustParseAddr(fmt.Sprintf("%s.%d", prefix, i/4+1))
			query := dnsQuery("example.com")
			binary.BigEndian.PutUint16(query[:2], uint16(i+1))
			<-start
			reply := s.resolve(context.Background(), query, source)
			if !matchingQuestion(query, reply) || binary.BigEndian.Uint16(reply[:2]) != uint16(i+1) || reply[3]&15 != 0 {
				t.Errorf("wrong answer or transaction ID for device %d", i)
			}
		}(i)
	}
	close(start)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("resolver request did not start")
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.activeQueries.Load() != clients && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.activeQueries.Load() != clients {
		t.Fatal("devices did not reach the blocked resolver")
	}
	// Allow the last scheduled caller to join the pending lookup.
	time.Sleep(30 * time.Millisecond)
	unblock.Do(func() { close(release) })
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("same WAN resolver received %d concurrent requests; want 1", got)
	}
	if len(s.cache) != 0 {
		t.Fatal("zero-TTL burst was cached")
	}
	s.resolve(context.Background(), dnsQuery("example.com"), netip.MustParseAddr("192.0.2.9"))
	if calls.Load() != 2 {
		t.Fatal("completed zero-TTL lookup was retained")
	}
}

func TestWANCacheSharedAcrossPoliciesPreservesTTLAndOwnedReplies(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(cacheableDoHReply(query, 42))
	}))
	defer upstream.Close()
	s := wanSharingServer(t, upstream.Client(), upstream.URL+"/dns-query")
	query := dnsQuery("example.com")
	s.resolve(context.Background(), query, netip.MustParseAddr("198.51.100.8"))
	for _, entry := range s.cache {
		entry.stored = entry.stored.Add(-10 * time.Second)
	}
	for i, source := range []string{"192.0.2.8", "203.0.113.8", "127.0.0.1", "198.51.100.9"} {
		binary.BigEndian.PutUint16(query[:2], uint16(100+i))
		reply := s.resolve(context.Background(), query, netip.MustParseAddr(source))
		if binary.BigEndian.Uint16(reply[:2]) != uint16(100+i) || reply[len(reply)-1] != 42 {
			t.Fatal("shared WAN cache corrupted a client's response")
		}
		if ttl := binary.BigEndian.Uint32(reply[len(query)+6:]); ttl > 20 || ttl < 18 {
			t.Fatalf("shared response did not age its TTL: %d", ttl)
		}
		reply[len(reply)-1] = 99
	}
	if calls.Load() != 1 || len(s.cache) != 1 {
		t.Fatalf("same WAN route duplicated cache or upstream work: calls=%d entries=%d", calls.Load(), len(s.cache))
	}
}

func TestWANSharingDoesNotCrossProxyRoutesOrRecoveredFallback(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(cacheableDoHReply(query, byte(call)))
	}))
	defer upstream.Close()
	s := wanSharingServer(t, upstream.Client(), upstream.URL+"/dns-query")
	s.prefixes = append(s.prefixes, policyPrefix{"strict", netip.MustParsePrefix("192.0.3.0/24")})
	query := dnsQuery("example.com")
	for i, source := range []string{"198.51.100.8", "192.0.3.8"} {
		answer := s.resolve(context.Background(), query, netip.MustParseAddr(source))
		if answer[len(answer)-1] != byte(i+1) {
			t.Fatal("a strict proxy request consumed the WAN cache")
		}
	}
	wan := s.resolve(context.Background(), query, netip.MustParseAddr("203.0.113.8"))
	if wan[len(wan)-1] != 1 || calls.Load() != 2 {
		t.Fatal("permitted WAN fallback did not share the direct WAN answer")
	}
	s.stateValid = false
	stale := s.resolve(context.Background(), query, netip.MustParseAddr("203.0.113.8"))
	if stale[len(stale)-1] != 3 || calls.Load() != 3 {
		t.Fatal("stale health authorized a cached WAN answer")
	}
	s.stateValid = true
	s.ruleWANActive["resproxy-fallback"] = false
	proxy := s.resolve(context.Background(), query, netip.MustParseAddr("203.0.113.8"))
	if proxy[len(proxy)-1] != 3 || calls.Load() != 3 {
		t.Fatal("recovered proxy reused the cached WAN fallback answer")
	}
}

func TestWANSharedCacheSeparatesQuestionFlagsAndEDNSSubnet(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query, _ := io.ReadAll(r.Body)
		end, ok := skipName(query, 12)
		if !ok || end+4 > len(query) {
			t.Error("malformed test question")
			return
		}
		end += 4
		body := append([]byte(nil), query[:end]...)
		body[2] |= 0x80
		binary.BigEndian.PutUint16(body[6:8], 1)
		typ := binary.BigEndian.Uint16(query[end-4:])
		body = append(body, 0xc0, 0x0c, byte(typ>>8), byte(typ), 0, 1, 0, 0, 0, 30)
		if typ == 28 {
			body = append(body, 0, 16, 0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 42)
		} else {
			body = append(body, 0, 4, 192, 0, 2, 42)
		}
		body = append(body, query[end:]...) // Additional OPT follows the answer.
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	s := wanSharingServer(t, upstream.Client(), upstream.URL+"/dns-query")
	queries := make([][]byte, 7)
	for i := range queries {
		queries[i] = dnsQuery("example.com")
	}
	binary.BigEndian.PutUint16(queries[1][len(queries[1])-4:], 28)
	queries[2][3] |= 0x10 // Checking Disabled.
	queries[3][2] = 0     // Recursion Desired is absent.
	queries[4][11] = 1
	queries[4] = append(queries[4], 0, 0, 41, 16, 0, 0, 0, 0x80, 0, 0, 0) // DNSSEC OK.
	for i := 5; i < 7; i++ {
		queries[i][11] = 1
		// EDNS Client Subnet: IPv4 /24, differing network bytes.
		queries[i] = append(queries[i], 0, 0, 41, 16, 0, 0, 0, 0, 0, 0, 11, 0, 8, 0, 7, 0, 1, 24, 0, 192, 0, byte(i))
	}
	for i, query := range queries {
		answer := s.resolve(context.Background(), query, netip.MustParseAddr("198.51.100.8"))
		if !matchingQuestion(query, answer) || calls.Load() != int32(i+1) {
			t.Fatalf("question type, DNS flags or EDNS options were merged: variant=%d calls=%d", i, calls.Load())
		}
	}
	for i, query := range queries {
		binary.BigEndian.PutUint16(query[:2], uint16(i+500))
		answer := s.resolve(context.Background(), query, netip.MustParseAddr("192.0.2.8"))
		if !matchingQuestion(query, answer) || binary.BigEndian.Uint16(answer[:2]) != uint16(i+500) {
			t.Fatal("variant's shared response has wrong question or transaction ID")
		}
	}
	if calls.Load() != int32(len(queries)) || len(s.cache) != len(queries) {
		t.Fatal("distinct variants were not cached independently and shared across WAN policies")
	}
}

func TestProxyDevicesShareOneRouteCacheEntry(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(cacheableDoHReply(query, 42))
	}))
	defer upstream.Close()
	s := &server{
		config:          Config{DefaultPolicy: "main", ProxyURL: upstream.URL + "/dns-query"},
		proxy:           upstream.Client(),
		proxySelections: map[string]string{"resproxy-main": "same-exit"},
	}
	for i := 1; i <= 48; i++ {
		query := dnsQuery("example.com")
		binary.BigEndian.PutUint16(query[:2], uint16(i))
		source := netip.AddrFrom4([4]byte{192, 0, 2, byte(i)})
		answer := s.resolve(context.Background(), query, source)
		if !matchingQuestion(query, answer) || binary.BigEndian.Uint16(answer[:2]) != uint16(i) {
			t.Fatalf("device %d received another device's answer", i)
		}
	}
	if calls.Load() != 1 || len(s.cache) != 1 || s.cacheHits.Load() != 47 {
		t.Fatalf("devices duplicated one proxy route: calls=%d entries=%d hits=%d", calls.Load(), len(s.cache), s.cacheHits.Load())
	}
}
