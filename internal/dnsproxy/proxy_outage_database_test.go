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
)

func TestDatabaseOutageDNSCoversUnmatchedNamesAndRecovery(t *testing.T) {
	var proxyCount, wanCount atomic.Int32
	respond := func(counter *atomic.Int32) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			body, _ := io.ReadAll(r.Body)
			body[2], body[3] = 0x81, 0x80
			w.Header().Set("Content-Type", "application/dns-message")
			w.Write(body)
		})
	}
	proxy := httptest.NewTLSServer(respond(&proxyCount))
	defer proxy.Close()
	wan := httptest.NewTLSServer(respond(&wanCount))
	defer wan.Close()
	m, err := compileMatcher(Policy{ProxyOutageAction: "wan", Rules: []Rule{{Action: "direct", Domains: []string{"direct.test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	strict, _ := compileMatcher(Policy{ProxyOutageAction: "block"})
	path := filepath.Join(t.TempDir(), "health.json")
	s := &server{config: Config{Mode: "proxy-only", DefaultPolicy: "pc", HealthStatePath: path, ProxyURL: proxy.URL + "/dns-query", DirectURL: wan.URL + "/dns-query"}, proxy: proxy.Client(), direct: wan.Client(), cache: map[string]cacheEntry{}, matchers: map[string]domainMatcher{"pc": m, "other": strict}, prefixes: []policyPrefix{{id: "other", prefix: netip.MustParsePrefix("192.0.2.0/24")}}}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		s.readState()
	}
	write(`{"resproxy-pc":{"selected":"block","runtime_selected":"block","runtime_confirmed":true,"wan_fallback_active":true},"resproxy-other":{"selected":"block","runtime_selected":"block","runtime_confirmed":true,"wan_fallback_active":true}}`)
	for _, test := range []struct{ name, source string }{{"unmatched.test", "198.51.100.1"}, {"direct.test", "198.51.100.1"}, {"other.test", "192.0.2.1"}} {
		a := s.resolve(context.Background(), dnsQuery(test.name), netip.MustParseAddr(test.source))
		if a[3]&15 != 0 {
			t.Fatal("DNS response failed")
		}
	}
	if wanCount.Load() != 2 || proxyCount.Load() != 1 {
		t.Fatalf("scope: WAN %d proxy %d", wanCount.Load(), proxyCount.Load())
	}
	write(`{"resproxy-pc":{"selected":"node","runtime_selected":"node","runtime_confirmed":true,"wan_fallback_active":false}}`)
	s.resolve(context.Background(), dnsQuery("unmatched.test"), netip.MustParseAddr("198.51.100.1"))
	if wanCount.Load() != 2 || proxyCount.Load() != 2 {
		t.Fatal("recovery reused WAN cache/route")
	}
	write(`{"resproxy-pc":{"selected":"block","runtime_selected":"block","runtime_confirmed":false,"wan_fallback_active":true}}`)
	if s.useRuleWAN("pc", "unmatched.test") {
		t.Fatal("unconfirmed state opened WAN")
	}
}
