package dnsproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuleWANDNSRequiresConfirmedOutageAndFirstMatch(t *testing.T) {
	m, err := compileMatcher(Policy{Rules: []Rule{
		{Action: "proxy", Domains: []string{"full:private.example.com"}},
		{Action: "proxy", OutageAction: "wan", Domains: []string{"example.com"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "health.json")
	s := &server{config: Config{Mode: "proxy-only", HealthStatePath: path}, matchers: map[string]domainMatcher{"pc": m, "other": m}}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		s.readState()
	}
	write(`{"resproxy-pc":{"selected":"block","runtime_selected":"block","runtime_confirmed":true,"wan_fallback_active":true}}`)
	if !s.useRuleWAN("pc", "www.example.com") || s.useRuleWAN("pc", "private.example.com") || s.useRuleWAN("pc", "unmatched.test") || s.useRuleWAN("other", "www.example.com") {
		t.Fatal("domain rule scope/order or strict DNS mode changed")
	}
	write(`{"resproxy-pc":{"selected":"node","runtime_selected":"node","runtime_confirmed":true,"wan_fallback_active":true}}`)
	if s.useRuleWAN("pc", "www.example.com") {
		t.Fatal("recovered proxy still resolves fallback domains via WAN")
	}
	write(`{"resproxy-pc":{"selected":"block","runtime_selected":"block","runtime_confirmed":false,"wan_fallback_active":true}}`)
	if s.useRuleWAN("pc", "www.example.com") {
		t.Fatal("unconfirmed state authorized WAN")
	}
	write(`{"resproxy-pc":{"selected":"block","runtime_selected":"block","runtime_confirmed":true,"wan_fallback_active":true}}`)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(path, old, old)
	s.readState()
	if s.useRuleWAN("pc", "www.example.com") {
		t.Fatal("stale state authorized WAN")
	}
}

// A saved global WAN mode from an older release must not authorize DNS
// outside the explicit rule, including strict domains and other devices.
func TestLegacyGlobalDNSModeCannotBypassRuleOutagePolicy(t *testing.T) {
	var proxyCalls, wanCalls atomic.Int32
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
	}))
	defer proxy.Close()
	wan := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wanCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		body[2], body[3] = 0x81, 0x80
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(body)
	}))
	defer wan.Close()
	path := filepath.Join(t.TempDir(), "health.json")
	if err := os.WriteFile(path, []byte(`{"resproxy-pc":{"selected":"block","runtime_selected":"block","runtime_confirmed":true,"wan_fallback_active":true},"resproxy-other":{"selected":"block","runtime_selected":"block","runtime_confirmed":true,"wan_fallback_active":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	matcher, err := compileMatcher(Policy{Rules: []Rule{
		{Action: "proxy", Domains: []string{"full:strict.example.com"}},
		{Action: "proxy", OutageAction: "wan", Domains: []string{"example.com"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{config: Config{Mode: "proxy-fallback-wan", DefaultPolicy: "pc", HealthStatePath: path, ProxyURL: proxy.URL + "/dns-query", DirectURL: wan.URL + "/dns-query"},
		proxy: proxy.Client(), direct: wan.Client(), cache: make(map[string]*cacheEntry),
		matchers: map[string]domainMatcher{"pc": matcher}, prefixes: []policyPrefix{{id: "other", prefix: netip.MustParsePrefix("192.0.2.0/24")}}}
	s.readState()
	for _, test := range []struct {
		name, source string
		rcode        byte
	}{
		{"www.example.com", "198.51.100.1", 0},
		{"strict.example.com", "198.51.100.1", 2},
		{"unmatched.test", "198.51.100.1", 2},
		{"www.example.com", "192.0.2.1", 2},
	} {
		answer := s.resolve(context.Background(), dnsQuery(test.name), netip.MustParseAddr(test.source))
		if len(answer) < 12 || answer[3]&15 != test.rcode {
			t.Fatalf("wrong DNS route for %s from %s: %x", test.name, test.source, answer)
		}
	}
	if wanCalls.Load() != 1 || proxyCalls.Load() != 3 {
		t.Fatalf("legacy global fallback bypassed rule scope: WAN=%d proxy=%d", wanCalls.Load(), proxyCalls.Load())
	}
}
