package dnsproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mixedDNSFixture(t *testing.T, budget int64) (*server, context.Context, func(), *atomic.Int32) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		query, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/proxy" {
			calls.Add(1)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/dns-message"}}, Body: io.NopCloser(bytes.NewReader(cacheableDoHReply(query, 42)))}, nil
	})}
	s := &server{requestBudget: budget, config: Config{DefaultPolicy: "main", Mode: "proxy-only", ProxyURL: "https://resolver.test/proxy", DirectURL: "https://resolver.test/wan"}, proxy: client, direct: client, matchers: make(map[string]domainMatcher)}
	var err error
	s.matchers["main"], err = compileMatcher(Policy{DirectDomains: []string{"fast.test"}})
	if err != nil {
		t.Fatal(err)
	}
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { cancel(); unblock(); s.requests().close(); s.workers.Wait() })
	return s, ctx, unblock, &calls
}

func waitDNSStats(t *testing.T, s *server, ready func(map[string]uint64) bool) map[string]uint64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stats := s.requests().stats()
		if ready(stats) {
			return stats
		}
		if time.Now().After(deadline) {
			t.Fatalf("DNS scheduler did not reach expected state: %v", stats)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSlowProxyAndSharedWaitersCannotStarveWAN(t *testing.T) {
	s, ctx, unblock, calls := mixedDNSFixture(t, 256<<10)
	const unique, duplicates = 128, 64
	finished := make(chan []byte, unique+duplicates)
	source := netip.MustParseAddr("192.0.2.8")
	for i := 0; i < unique+duplicates; i++ {
		name := fmt.Sprintf("slow-%d.test", i)
		if i >= unique {
			name = "slow-0.test"
		}
		query := dnsQuery(name)
		binary.BigEndian.PutUint16(query[:2], uint16(i+1))
		s.resolveAsync(ctx, query, source, func(body []byte) { finished <- body })
	}
	stats := waitDNSStats(t, s, func(m map[string]uint64) bool {
		return m["active_upstream"] > 0 && m["queued_upstream"] > 0 && m["waiting_shared"] == duplicates
	})
	if stats["dns_work_bytes"] > 256<<10 || stats["pending_query_bytes"] > 128<<10 || stats["dns_admission_rejected"] != 0 {
		t.Fatalf("unexpected memory pressure: %v", stats)
	}
	const fast = 32
	holdFast := make(chan struct{})
	releaseFast := sync.OnceFunc(func() { close(holdFast) })
	defer releaseFast()
	latency := make(chan time.Duration, fast)
	errors := make(chan string, fast)
	for i := 0; i < fast; i++ {
		query := dnsQuery(fmt.Sprintf("q%d.fast.test", i))
		start := time.Now()
		s.resolveAsync(ctx, query, source, func(body []byte) {
			if len(body) < 12 || body[3]&15 != 0 {
				errors <- fmt.Sprintf("fast WAN request failed: bytes=%d stats=%v transport=%d invalid=%d timeouts=%d", len(body), s.requests().stats(), s.upstreamTransport.Load(), s.upstreamInvalid.Load(), s.upstreamTimeouts.Load())
			} else {
				// Hold completed response charges while every recipient joins.
				// Admission must not depend on a fast callback winning a race.
				<-holdFast
			}
			latency <- time.Since(start)
		})
	}
	releaseFast()
	delays := make([]time.Duration, 0, fast)
	for i := 0; i < fast; i++ {
		select {
		case d := <-latency:
			delays = append(delays, d)
		case <-time.After(time.Second):
			t.Fatal("WAN waited behind blocked proxy operations")
		}
	}
	if len(errors) > 0 {
		t.Fatal(<-errors)
	}
	sort.Slice(delays, func(i, j int) bool { return delays[i] < delays[j] })
	t.Logf("WAN during blocked proxy burst: p50=%v p95=%v max=%v, proxy active=%d, shared waiters=%d, work bytes=%d", delays[fast/2], delays[fast*95/100], delays[fast-1], calls.Load(), stats["waiting_shared"], stats["dns_work_bytes"])
	unblock()
	ids := make(map[uint16]bool)
	for i := 0; i < unique+duplicates; i++ {
		select {
		case body := <-finished:
			if len(body) < 12 || body[3]&15 != 0 {
				t.Fatal("slow/shared response failed")
			}
			id := binary.BigEndian.Uint16(body[:2])
			if ids[id] {
				t.Fatal("recipient transaction IDs aliased")
			}
			ids[id] = true
		case <-time.After(time.Second):
			t.Fatal("slow requests did not drain")
		}
	}
	if calls.Load() != unique {
		t.Fatalf("duplicate recipients created %d upstream requests, want %d", calls.Load(), unique)
	}
	waitDNSStats(t, s, func(m map[string]uint64) bool { return m["dns_work_bytes"] == 0 })
}

func TestSharedWaiterMemoryPressurePreservesOtherPaths(t *testing.T) {
	s, ctx, unblock, calls := mixedDNSFixture(t, 256<<10)
	query := dnsQuery("same.slow.test")
	source := netip.MustParseAddr("192.0.2.8")
	var good, rejected atomic.Int32
	for i := 0; i < 2000; i++ {
		s.resolveAsync(ctx, query, source, func(body []byte) {
			if body[3]&15 == 0 {
				good.Add(1)
			} else {
				rejected.Add(1)
			}
		})
	}
	stats := s.requests().stats()
	if stats["dns_admission_rejected"] == 0 || stats["dns_work_bytes"] > 256<<10 || stats["pending_query_bytes"] > 128<<10 {
		t.Fatalf("waiting recipients exceeded the memory budget: %v", stats)
	}
	answer := s.resolve(ctx, dnsQuery("fast.test"), source)
	if answer[3]&15 != 0 {
		t.Fatal("a full shared-recipient queue blocked WAN")
	}
	unblock()
	s.workers.Wait()
	if calls.Load() != 1 || good.Load() == 0 || rejected.Load() == 0 || good.Load()+rejected.Load() != 2000 {
		t.Fatalf("unexpected fanout/accounting: upstream=%d good=%d pressure=%d", calls.Load(), good.Load(), rejected.Load())
	}
}

func TestQueuedDNSCancellationReleasesAllMemory(t *testing.T) {
	s, _, _, _ := mixedDNSFixture(t, 2*dnsRequestMemory)
	ctx, cancel := context.WithCancel(context.Background())
	for i := 0; i < 12; i++ {
		s.resolveAsync(ctx, dnsQuery(fmt.Sprintf("slow-%d.test", i)), netip.MustParseAddr("192.0.2.8"), func([]byte) {})
	}
	waitDNSStats(t, s, func(m map[string]uint64) bool { return m["active_upstream"] > 0 && m["queued_upstream"] > 0 })
	cancel()
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled DNS queue retained workers")
	}
	stats := s.requests().stats()
	if stats["dns_work_bytes"] != 0 || stats["pending_query_bytes"] != 0 || s.activeQueries.Load() != 0 {
		t.Fatalf("canceled DNS retained memory: %v", stats)
	}
}

func TestUDPDuplicatesUseOneUpstreamWorker(t *testing.T) {
	s, ctx, unblock, calls := mixedDNSFixture(t, 256<<10)
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// The real UDP listener sees loopback clients as router/WAN traffic.
	s.config.DirectURL = s.config.ProxyURL
	s.workers.Add(1)
	go func() { defer s.workers.Done(); s.serveUDP(ctx, conn) }()
	client, err := net.Dial("udp4", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	before := runtime.NumGoroutine()
	for i := 0; i < 64; i++ {
		query := dnsQuery("same.test")
		binary.BigEndian.PutUint16(query[:2], uint16(i))
		if _, err := client.Write(query); err != nil {
			t.Fatal(err)
		}
	}
	stats := waitDNSStats(t, s, func(m map[string]uint64) bool { return m["waiting_shared"] == 63 })
	if stats["active_upstream"] != 1 || calls.Load() != 1 {
		t.Fatalf("UDP copies created separate workers: %v calls=%d", stats, calls.Load())
	}
	t.Logf("64 pending UDP recipients: goroutine delta=%d, pending bytes=%d, upstream=%d", runtime.NumGoroutine()-before, stats["pending_query_bytes"], stats["active_upstream"])
	unblock()
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 4096)
	ids := map[uint16]bool{}
	for i := 0; i < 64; i++ {
		n, err := client.Read(buffer)
		if err != nil {
			t.Fatal(err)
		}
		if n < 12 || buffer[3]&15 != 0 {
			t.Fatal("invalid shared UDP response")
		}
		id := binary.BigEndian.Uint16(buffer[:2])
		if ids[id] {
			t.Fatal("UDP response ID duplicated")
		}
		ids[id] = true
	}
	conn.Close()
}

func TestQueuedDNSDeadlineDoesNotOpenAnotherUpstream(t *testing.T) {
	s, ctx, _, calls := mixedDNSFixture(t, 2*dnsRequestMemory)
	source := netip.MustParseAddr("192.0.2.8")
	s.resolveAsync(ctx, dnsQuery("blocking.test"), source, func([]byte) {})
	waitDNSStats(t, s, func(m map[string]uint64) bool { return m["active_upstream"] == 1 })
	queuedCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	done := make(chan []byte, 1)
	s.resolveAsync(queuedCtx, dnsQuery("queued.test"), source, func(body []byte) { done <- body })
	select {
	case body := <-done:
		if body[3]&15 != 2 {
			t.Fatal("expired queued query did not fail closed")
		}
	case <-time.After(time.Second):
		t.Fatal("queue ignored deadline")
	}
	if calls.Load() != 1 || s.upstreamTimeouts.Load() != 0 {
		t.Fatal("expired queue opened or blamed an upstream exchange")
	}
	waitDNSStats(t, s, func(m map[string]uint64) bool { return m["queued_upstream"] == 0 && m["dns_queue_timeouts"] == 1 })
}
