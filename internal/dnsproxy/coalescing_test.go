package dnsproxy

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise a real cold TLS resolver, including distinct DNS transaction IDs.
func TestColdDNSBurstSharesLookupAndPreservesClientIDs(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(dohReply(query)) // No cacheable answer: this verifies in-flight sharing.
	}))
	defer upstream.Close()
	s := &server{config: Config{DefaultPolicy: "main", Mode: "proxy-only", ProxyURL: upstream.URL + "/dns-query"}, proxy: upstream.Client()}
	const clients = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(id uint16) {
			defer wg.Done()
			query := dnsQuery("example.com")
			binary.BigEndian.PutUint16(query[:2], id)
			<-start
			source := netip.AddrFrom4([4]byte{192, 0, 2, byte(id)})
			reply := s.resolve(context.Background(), query, source)
			if !matchingQuestion(query, reply) || reply[3]&15 != 0 {
				t.Errorf("invalid response for client %d", id)
			}
		}(uint16(i + 1))
	}
	close(start)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("cold burst opened %d upstream requests; want 1", got)
	}
	// In-flight sharing must neither cache a zero-TTL answer nor retain entries.
	query := dnsQuery("example.com")
	if reply := s.resolve(context.Background(), query, netip.MustParseAddr("192.0.2.8")); !matchingQuestion(query, reply) {
		t.Fatal("next lookup failed")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("completed lookup was retained: %d requests", got)
	}
}

func TestColdDNSSharingSeparatesPolicyAndQuestionType(t *testing.T) {
	var calls atomic.Int32
	arrived := make(chan struct{}, 3)
	release := make(chan struct{})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query, _ := io.ReadAll(r.Body)
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(dohReply(query))
	}))
	defer upstream.Close()
	defer close(release)
	s := &server{config: Config{DefaultPolicy: "main", Mode: "proxy-only", ProxyURL: upstream.URL + "/dns-query"}, proxy: upstream.Client(),
		prefixes: []policyPrefix{{"other", netip.MustParsePrefix("192.0.2.0/24")}}}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := dnsQuery("example.com")
			source := netip.MustParseAddr("198.51.100.8")
			if i == 1 {
				source = netip.MustParseAddr("192.0.2.8")
			}
			if i == 2 {
				binary.BigEndian.PutUint16(q[len(q)-4:len(q)-2], 28)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			s.resolve(ctx, q, source)
		}(i)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("different policy or question incorrectly shared")
		}
	}
	// Unblock all requests before waiting; the deferred close remains for failure.
	for i := 0; i < 3; i++ {
		release <- struct{}{}
	}
	wg.Wait()
	if calls.Load() != 3 {
		t.Fatal("lookup isolation failed")
	}
}

func TestCanceledColdDNSBurstDrainsAndDoesNotRetainFailure(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	s := &server{config: Config{DefaultPolicy: "main", Mode: "proxy-only", ProxyURL: "https://resolver.test/dns-query"}, proxy: client}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id uint16) {
			defer wg.Done()
			q := dnsQuery("example.com")
			binary.BigEndian.PutUint16(q[:2], id)
			reply := s.resolve(ctx, q, netip.MustParseAddr("192.0.2.8"))
			if binary.BigEndian.Uint16(reply[:2]) != id || reply[3]&15 != 2 {
				t.Error("canceled query did not fail closed with client ID")
			}
		}(uint16(i + 1))
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("lookup did not start")
	}
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled burst did not drain")
	}
	before := calls.Load()
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer retryCancel()
	s.resolve(retryCtx, dnsQuery("example.com"), netip.MustParseAddr("192.0.2.8"))
	if calls.Load() != before+1 {
		t.Fatal("failed lookup prevented next upstream attempt")
	}
}
