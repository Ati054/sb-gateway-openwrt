package dnsproxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func cacheableDoHReply(query []byte, value byte) []byte {
	body := dohReply(query)
	binary.BigEndian.PutUint16(body[6:8], 1)
	return append(body, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4, 192, 0, 2, value)
}
func TestDNSSelectedRouteChangeRetiresOnlyItsTransportAndCache(t *testing.T) {
	var connections, calls atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(cacheableDoHReply(query, byte(call)))
	}))
	upstream.EnableHTTP2 = true
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	upstream.StartTLS()
	defer upstream.Close()
	transport := upstream.Client().Transport.(*http.Transport)
	main, other := newDoHClient(transport.Clone()), newDoHClient(transport.Clone())
	defer main.CloseIdleConnections()
	defer other.CloseIdleConnections()
	path := filepath.Join(t.TempDir(), "health.json")
	s := &server{config: Config{DefaultPolicy: "main", Mode: "proxy-only", ProxyURL: upstream.URL + "/dns-query", HealthStatePath: path}, proxy: main, policyProxies: map[string]*http.Client{"main": main, "other": other}}
	write := func(node string) {
		t.Helper()
		body := `{"resproxy-main":{"selected":"` + node + `","runtime_selected":"` + node + `","runtime_confirmed":true},"resproxy-other":{"selected":"b","runtime_selected":"b","runtime_confirmed":true}}`
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		s.readState()
	}
	write("a")
	source := netip.MustParseAddr("192.0.2.10")
	first := s.resolve(context.Background(), dnsQuery("example.com"), source)
	if first[len(first)-1] != 1 {
		t.Fatalf("initial DNS resolution failed: %v", first)
	}
	if cached := s.resolve(context.Background(), dnsQuery("example.com"), source); cached[len(cached)-1] != 1 || calls.Load() != 1 {
		t.Fatal("unchanged route lost valid cache")
	}
	otherOwner := other.Transport.(*recoveringTransport)
	previousOther := otherOwner.current
	if _, err := exchangeDoH(context.Background(), other, upstream.URL, dnsQuery("other.example")); err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 2 {
		t.Fatal("fixture did not establish isolated policy connections")
	}
	write("c")
	if otherOwner.current != previousOther {
		t.Fatal("one sheet change retired another sheet's resolver")
	}
	fresh := s.resolve(context.Background(), dnsQuery("example.com"), source)
	if fresh[len(fresh)-1] != 3 || calls.Load() != 3 || connections.Load() != 3 {
		t.Fatal("selected route change reused old DNS answer or tunnel")
	}
	if _, err := exchangeDoH(context.Background(), other, upstream.URL, dnsQuery("other2.example")); err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 3 {
		t.Fatal("unchanged sheet did not retain its HTTP2 connection")
	}
}
func TestDoHTLSResumesAfterTransportRetirement(t *testing.T) {
	var resumed atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS.DidResume {
			resumed.Add(1)
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(dohReply(body))
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	client := newDoHClient(upstream.Client().Transport.(*http.Transport).Clone())
	defer client.CloseIdleConnections()
	owner := client.Transport.(*recoveringTransport)
	for i := 0; i < 2; i++ {
		if _, err := exchangeDoH(context.Background(), client, upstream.URL, dnsQuery("example.com")); err != nil {
			t.Fatal(err)
		}
		owner.retire(owner.current)
	}
	if resumed.Load() == 0 {
		t.Fatal("replacement DoH transport paid a full TLS handshake instead of resuming")
	}
}

func TestRetiredRouteInflightDNSCannotPopulateCurrentCache(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	defer upstream.Close()
	defer once.Do(func() { close(release) })
	client := newDoHClient(upstream.Client().Transport.(*http.Transport).Clone())
	defer client.CloseIdleConnections()
	path := filepath.Join(t.TempDir(), "health.json")
	s := &server{config: Config{DefaultPolicy: "main", Mode: "proxy-only", ProxyURL: upstream.URL + "/dns-query", HealthStatePath: path}, proxy: client}
	write := func(node string) {
		t.Helper()
		body := `{"resproxy-main":{"selected":"` + node + `","runtime_selected":"` + node + `","runtime_confirmed":true}}`
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		s.readState()
	}
	write("a")
	done := make(chan []byte, 1)
	go func() {
		done <- s.resolve(context.Background(), dnsQuery("example.com"), netip.MustParseAddr("192.0.2.10"))
	}()
	select {
	case <-started:
	case <-done:
		t.Fatal("old route request failed before reaching resolver")
	}
	write("b")
	once.Do(func() { close(release) })
	old := <-done
	if old[len(old)-1] != 1 {
		t.Fatal("route retirement aborted healthy inflight response")
	}
	if len(s.cache) != 0 {
		t.Fatal("retired route request repopulated cache after route change")
	}
	fresh := s.resolve(context.Background(), dnsQuery("example.com"), netip.MustParseAddr("192.0.2.10"))
	if fresh[len(fresh)-1] != 2 || calls.Load() != 2 {
		t.Fatal("new route consumed old DNS evidence")
	}
}
