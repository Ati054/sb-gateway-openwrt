package dnsproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/healthcontract"
)

func TestUnconfirmedObservationPreservesRouteScopedDNSCache(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(cacheableDoHReply(query, 42))
	}))
	defer upstream.Close()
	client := newDoHClient(upstream.Client().Transport.(*http.Transport).Clone())
	defer client.CloseIdleConnections()
	path := filepath.Join(t.TempDir(), "runtime.json")
	s := &server{config: Config{DefaultPolicy: "main", HealthStatePath: path, ProxyURL: upstream.URL + "/dns-query"}, proxy: client, cacheBudget: 4096}
	runtimeFixture(t, path, "leaf", time.Now())
	s.readState()
	query := dnsQuery("example.test")
	resolve := func() {
		t.Helper()
		if answer := s.resolve(context.Background(), query, netip.MustParseAddr("192.0.2.20")); len(answer) < 12 || answer[3]&15 != 0 {
			t.Fatal("DNS lookup failed")
		}
	}
	resolve()
	if calls.Load() != 1 {
		t.Fatal("cold query did not reach resolver")
	}
	if err := os.WriteFile(path, []byte(`{"resproxy-main":{"selected":"leaf","runtime_selected":"","runtime_confirmed":false,"wan_fallback_active":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.readState()
	resolve()
	if calls.Load() != 1 || s.cacheHits.Load() != 1 || s.ruleWANActive["resproxy-main"] {
		t.Fatal("observation timeout invalidated cached proxy DNS or enabled WAN")
	}
	runtimeFixture(t, path, "new", time.Now())
	s.readState()
	resolve()
	if calls.Load() != 2 {
		t.Fatal("changed proxy route reused prior route's cache")
	}
}

func TestUnconfirmedSameLeafDoesNotRebuildDNSRoute(t *testing.T) {
	for _, observed := range []string{"", "leaf"} {
		t.Run("observed-"+observed, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.json")
			client := newDoHClient(&http.Transport{})
			defer client.CloseIdleConnections()
			owner := client.Transport.(*recoveringTransport)
			s := &server{config: Config{HealthStatePath: path, DefaultPolicy: "main"}, proxy: client}
			write := func(selected, actual string, confirmed bool) {
				t.Helper()
				body, err := json.Marshal(healthcontract.RuntimeSnapshot{UpdatedAt: time.Now(), Policies: map[string]healthcontract.RuntimePolicy{"resproxy-main": {Selected: selected, RuntimeSelected: actual, RuntimeConfirmed: confirmed, WANFallbackActive: true}}})
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				s.readState()
			}
			write("leaf", "leaf", true)
			original := owner.current
			write("leaf", observed, false)
			if owner.current != original || s.proxySelections["resproxy-main"] != "leaf" {
				t.Fatal("transient observation invalidated an unchanged proxy session/cache scope")
			}
			if s.ruleWANActive["resproxy-main"] {
				t.Fatal("uncertain observation authorized WAN")
			}
			write("leaf", "leaf", true)
			if owner.current != original {
				t.Fatal("reconfirmation rebuilt the same route")
			}
			write("new", "new", true)
			if owner.current == original || s.proxySelections["resproxy-main"] != "new" {
				t.Fatal("confirmed route change did not retire old session")
			}
		})
	}
}

func TestUnconfirmedRouteChangeDoesNotRetainOldDNSSelection(t *testing.T) {
	for _, change := range []struct{ name, selected, observed string }{
		{"blocked", "block", ""}, {"changed", "new", ""}, {"mismatch", "leaf", "new"}, {"missing", "", ""},
	} {
		t.Run(change.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.json")
			s := &server{config: Config{HealthStatePath: path}, proxySelections: map[string]string{"resproxy-main": "leaf"}}
			body, err := json.Marshal(healthcontract.RuntimeSnapshot{UpdatedAt: time.Now(), Policies: map[string]healthcontract.RuntimePolicy{"resproxy-main": {Selected: change.selected, RuntimeSelected: change.observed, RuntimeConfirmed: false, WANFallbackActive: true}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			s.readState()
			if s.proxySelections["resproxy-main"] != "" || s.ruleWANActive["resproxy-main"] {
				t.Fatal("changed or uncertain route retained prior authorization")
			}
		})
	}
}
