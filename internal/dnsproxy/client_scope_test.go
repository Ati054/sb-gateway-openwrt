package dnsproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestUnassignedDNSClientsUseWANWithoutAnAllDeviceList(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		value := byte(1)
		if r.URL.Path == "/proxy" {
			value = 2
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(cacheableDoHReply(query, value))
	}))
	defer upstream.Close()
	s := wanSharingServer(t, upstream.Client(), upstream.URL+"/wan")
	s.config.ProxyURL = upstream.URL + "/proxy"
	s.config.Policies = []Policy{{ID: "main", SourceCIDRs: []string{"192.0.2.20/32", "2001:db8::20/128"}}}
	s.prefixes = []policyPrefix{{"main", netip.MustParsePrefix("192.0.2.20/32")}, {"main", netip.MustParsePrefix("2001:db8::20/128")}}
	var err error
	s.matchers["main"], err = compileMatcher(Policy{ID: "main"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ip   string
		want byte
	}{{"192.0.2.21", 1}, {"192.0.2.20", 2}, {"2001:db8::21", 1}, {"2001:db8::20", 2}, {"127.0.0.1", 1}} {
		answer := s.resolve(context.Background(), dnsQuery("example.com"), netip.MustParseAddr(tc.ip))
		if len(answer) == 0 || answer[len(answer)-1] != tc.want {
			t.Fatalf("client %s used the wrong DNS route", tc.ip)
		}
	}
	s.config.Policies = []Policy{{ID: "main"}}
	answer := s.resolve(context.Background(), dnsQuery("example.com"), netip.MustParseAddr("192.0.2.21"))
	if len(answer) == 0 || answer[len(answer)-1] != 2 {
		t.Fatal("explicit all-device policy lost proxy DNS")
	}
}
