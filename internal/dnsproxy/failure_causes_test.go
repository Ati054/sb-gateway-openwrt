package dnsproxy

import (
	"io"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestDNSFailureCausesSeparateRouteStateFromUpstream(t *testing.T) {
	var calls atomic.Int32
	s, publish := managedFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(cacheableDoHReply(query, 42))
	})
	source := netip.MustParseAddr("192.0.2.1")
	if body := routeAnswer(t, s, "before-ready.test", source); body[3]&15 != 2 {
		t.Fatal("unconfirmed route did not fail closed")
	}
	if s.failureRouteNotReady.Load() != 1 || s.upstreamTransport.Load() != 0 || calls.Load() != 0 {
		t.Fatal("route preparation was counted as an upstream network failure")
	}
	publish("resproxy-main", "block")
	if body := routeAnswer(t, s, "blocked.test", source); body[3]&15 != 2 || s.failureRouteBlocked.Load() != 1 {
		t.Fatal("blocked route cause was not separated")
	}
	publish("resproxy-main", "working")
	if body := routeAnswer(t, s, "working.test", source); body[3]&15 != 0 || calls.Load() != 1 {
		t.Fatal("confirmed route did not recover normally")
	}
	if s.failures.Load() != 2 || s.failureRouteNotReady.Load()+s.failureRouteBlocked.Load() != 2 {
		t.Fatal("diagnostics changed or double-counted failed queries")
	}
}
