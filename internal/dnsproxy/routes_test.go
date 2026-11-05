package dnsproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/dnsroute"
)

func managedFixture(t *testing.T, handler http.HandlerFunc) (*server, func(string, string)) {
	t.Helper()
	upstream := httptest.NewUnstartedServer(handler)
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	main := newDoHClient(upstream.Client().Transport.(*http.Transport).Clone())
	other := newDoHClient(upstream.Client().Transport.(*http.Transport).Clone())
	routes := dnsroute.New([]string{"resproxy-main", "resproxy-other"})
	core := dnsroute.Core{PID: 42, StartTicks: "10"}
	routes.SetCore(core)
	s := &server{config: Config{DefaultPolicy: "main", ProxyURL: upstream.URL + "/dns-query", DirectURL: upstream.URL + "/dns-query", Routes: routes}, proxy: main, direct: other,
		policyProxies: map[string]*http.Client{"other": other}, cacheBudget: 1 << 20}
	stop := routes.Subscribe(s.routeChanged)
	t.Cleanup(func() {
		stop()
		s.requests().close()
		s.workers.Wait()
		s.closeRouteClients()
		main.CloseIdleConnections()
		other.CloseIdleConnections()
	})
	return s, routes.Publisher(core)
}

func routeAnswer(t *testing.T, s *server, name string, source netip.Addr) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	answer := s.resolve(ctx, dnsQuery(name), source)
	if len(answer) < 12 {
		t.Fatal("short DNS response")
	}
	return answer
}

func TestManagedRouteEpochABASeparatesCacheFlightsAndTransports(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	s, publish := managedFixture(t, func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		call := calls.Add(1)
		if call == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(cacheableDoHReply(query, byte(call)))
	})
	publish("resproxy-main", "a")
	publish("resproxy-other", "other")
	s.routeMu.Lock()
	unaffected := s.routeClients["resproxy-other"].client
	first := s.routeClients["resproxy-main"].client
	s.routeMu.Unlock()
	old := make(chan []byte, 1)
	go func() {
		old <- s.resolve(context.Background(), dnsQuery("same.test"), netip.MustParseAddr("192.0.2.1"))
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old request did not start")
	}
	publish("resproxy-main", "b")
	publish("resproxy-main", "a")
	fresh := routeAnswer(t, s, "same.test", netip.MustParseAddr("192.0.2.2"))
	if fresh[len(fresh)-1] != 2 {
		t.Fatal("new query joined an old flight")
	}
	s.routeMu.Lock()
	current := s.routeClients["resproxy-main"].client
	other := s.routeClients["resproxy-other"].client
	s.routeMu.Unlock()
	if current == first || other != unaffected {
		t.Fatal("transport scope was not isolated")
	}
	once.Do(func() { close(release) })
	select {
	case response := <-old:
		if response[len(response)-1] != 1 {
			t.Fatal("healthy old response aborted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old request did not finish")
	}
	again := routeAnswer(t, s, "same.test", netip.MustParseAddr("192.0.2.3"))
	if again[len(again)-1] != 2 || calls.Load() != 2 {
		t.Fatal("old response overwrote new route cache")
	}
	publish("resproxy-main", "a")
	if routeAnswer(t, s, "same.test", netip.MustParseAddr("192.0.2.4"))[len(again)-1] != 2 || calls.Load() != 2 {
		t.Fatal("unchanged readback invalidated cache")
	}
}

func TestManagedRouteUnconfirmedAndWithdrawnFailClosedWANSurvives(t *testing.T) {
	var calls atomic.Int32
	s, publish := managedFixture(t, func(w http.ResponseWriter, r *http.Request) {
		q, _ := io.ReadAll(r.Body)
		calls.Add(1)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(cacheableDoHReply(q, 1))
	})
	source := netip.MustParseAddr("192.0.2.1")
	if answer := routeAnswer(t, s, "a.test", source); answer[3]&15 != 2 || calls.Load() != 0 {
		t.Fatal("unconfirmed proxy route accepted")
	}
	publish("resproxy-main", "a")
	if routeAnswer(t, s, "a.test", source)[3]&15 != 0 {
		t.Fatal("confirmed proxy failed")
	}
	wan := netip.MustParseAddr("127.0.0.1")
	if routeAnswer(t, s, "wan.test", wan)[3]&15 != 0 {
		t.Fatal("WAN failed")
	}
	s.config.Routes.SetCore(dnsroute.Core{})
	publish("resproxy-main", "a")
	if routeAnswer(t, s, "a.test", source)[3]&15 != 2 {
		t.Fatal("old publisher revived cache/route")
	}
	if routeAnswer(t, s, "wan.test", wan)[3]&15 != 0 || calls.Load() != 2 {
		t.Fatal("core withdrawal invalidated WAN cache")
	}
}

func TestManagedRouteChangeCancelsOldQueueWithoutNewExchange(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	s, publish := managedFixture(t, func(w http.ResponseWriter, r *http.Request) {
		q, _ := io.ReadAll(r.Body)
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(cacheableDoHReply(q, 1))
	})
	r := s.requests()
	r.budget = 2 * dnsRequestMemory // One running leader, bounded waiting queue.
	r.pendingLimit = r.budget / 2
	publish("resproxy-main", "a")
	source := netip.MustParseAddr("192.0.2.1")
	old, queued := make(chan []byte, 1), make(chan []byte, 1)
	go func() { old <- s.resolve(context.Background(), dnsQuery("active.test"), source) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("leader did not start")
	}
	s.resolveAsync(context.Background(), dnsQuery("queued.test"), source, func(b []byte) { queued <- b })
	if s.requests().stats()["queued_upstream"] != 1 {
		t.Fatal("fixture did not queue")
	}
	publish("resproxy-main", "b")
	select {
	case b := <-queued:
		if b[3]&15 != 2 {
			t.Fatal("stale queue did not fail closed")
		}
	case <-time.After(time.Second):
		t.Fatal("stale queue retained its budget")
	}
	if calls.Load() != 1 {
		t.Fatal("stale queued query reached upstream")
	}
	once.Do(func() { close(release) })
	<-old
	deadline := time.Now().Add(time.Second)
	for s.requests().stats()["dns_work_bytes"] != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.requests().stats()["dns_work_bytes"] != 0 {
		t.Fatal("route retirement leaked budget")
	}
}

func TestDNSListenerExplicitlyDisablesMultipath(t *testing.T) {
	t.Setenv("GODEBUG", "multipathtcp=3")
	config := dnsListenConfig()
	if config.MultipathTCP() {
		t.Fatal("DNS listener inherited MPTCP runtime default")
	}
}
