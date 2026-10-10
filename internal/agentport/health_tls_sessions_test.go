package agentport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAvailabilityTLSResumptionStillOpensFreshProxyConnections(t *testing.T) {
	// This verifies identity/session/tunnel semantics, not a latency bound.
	// Allow emulated ARM and loaded CI time to complete the fixture handshake.
	const fixtureTimeout = 10 * time.Second
	var mu sync.Mutex
	var resumed []bool
	var peers []string
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		resumed = append(resumed, r.TLS.DidResume)
		peers = append(peers, r.RemoteAddr)
		mu.Unlock()
		w.Write([]byte("available"))
	}))
	defer origin.Close()
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	sessions := tls.NewLRUClientSessionCache(8)
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", 400)
			return
		}
		upstream, err := net.DialTimeout("tcp", r.Host, fixtureTimeout)
		if err != nil {
			http.Error(w, "dial failed", 502)
			return
		}
		defer upstream.Close()
		downstream, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer downstream.Close()
		connects.Add(1)
		buffered.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		buffered.Flush()
		done := make(chan struct{})
		go func() { io.Copy(upstream, buffered); upstream.(*net.TCPConn).CloseWrite(); close(done) }()
		io.Copy(downstream, upstream)
		downstream.Close()
		<-done
	}))
	defer proxy.Close()

	// Fast availability checks resume TLS but cannot reuse the old HTTP tunnel.
	for i := 0; i < 2; i++ {
		_, err := requestThroughProxyWithTLS(context.Background(), proxy.URL, origin.URL, http.MethodGet, fixtureTimeout, 1024, 200, &tls.Config{RootCAs: roots, ClientSessionCache: sessions})
		if err != nil {
			t.Fatal(err)
		}
	}
	// The quality transport has no session cache, matching production HEAD.
	_, err := requestThroughProxyWithTLS(context.Background(), proxy.URL, origin.URL, http.MethodHead, fixtureTimeout, 0, 200, &tls.Config{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	// A wrong server identity remains a failure even with a warm session cache.
	_, err = requestThroughProxyWithTLS(context.Background(), proxy.URL, origin.URL, http.MethodGet, fixtureTimeout, 1024, 200, &tls.Config{RootCAs: roots, ClientSessionCache: sessions, ServerName: "wrong.invalid"})
	if err == nil {
		t.Fatal("resumption bypassed hostname verification")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resumed) != 3 || resumed[0] || !resumed[1] || resumed[2] {
		t.Fatalf("unexpected session reuse: %v", resumed)
	}
	if connects.Load() != 4 || peers[0] == peers[1] || peers[1] == peers[2] {
		t.Fatalf("probes reused a TCP tunnel: connects=%d peers=%v", connects.Load(), peers)
	}
}
