package agentport

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFreshPhysicalHEADUsesNewConnectionAndIncludesSetup(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	connections := map[string]bool{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodHead {
			t.Errorf("method=%s", r.Method)
		}
		mu.Lock()
		connections[r.RemoteAddr] = true
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			time.Sleep(30 * time.Millisecond)
		}
	}
	server.Start()
	defer server.Close()
	for i := 0; i < 2; i++ {
		started := time.Now()
		_, err := requestThroughProxyContext(context.Background(), server.URL, "http://probe.invalid/", http.MethodHead, time.Second, 0, http.StatusNoContent)
		if err != nil || time.Since(started) < 25*time.Millisecond {
			t.Fatalf("fresh request skipped setup: %v", err)
		}
	}
	if calls.Load() != 2 || len(connections) != 2 {
		t.Fatalf("fresh requests=%d connections=%d", calls.Load(), len(connections))
	}
}
