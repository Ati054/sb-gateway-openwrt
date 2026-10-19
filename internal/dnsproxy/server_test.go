package dnsproxy

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestRemovedDirectOnlyListenerModeIsRejected(t *testing.T) {
	_, _, err := Start(context.Background(), Config{
		Port: 1054, ProxyPort: 19080, DefaultPolicy: "main", Mode: "direct-only",
		ProxyURL: "https://1.1.1.1/dns-query", DirectURL: "https://1.1.1.1/dns-query",
	})
	if err == nil {
		t.Fatal("removed direct-only DNS listener mode was accepted")
	}
}

func dnsQuery(name string) []byte {
	query := []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range splitLabels(name) {
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	return append(query, 0, 0, 1, 0, 1)
}

func splitLabels(name string) []string {
	var labels []string
	for len(name) > 0 {
		for i := range name {
			if name[i] == '.' {
				labels = append(labels, name[:i])
				name = name[i+1:]
				goto next
			}
		}
		labels = append(labels, name)
		break
	next:
	}
	return labels
}

func TestQuestionNameAndLocal(t *testing.T) {
	query := dnsQuery("Router.LAN")
	name, valid := questionName(query)
	if !valid || name != "router.lan" || !isLocalName(name) {
		t.Fatalf("unexpected local query: %q %v", name, valid)
	}
	query[4], query[5] = 0, 2
	if _, valid := questionName(query); valid {
		t.Fatal("multiple questions accepted")
	}
	broken := dnsQuery("test.example")
	broken[12] = 0xc0
	if _, valid := questionName(broken); valid {
		t.Fatal("compressed query name accepted")
	}
	if isLocalName("notlan.example") || isLocalName("example.com") {
		t.Fatal("public domain classified as local")
	}
}

func TestServfailResponse(t *testing.T) {
	query := dnsQuery("example.com")
	response := servfail(query)
	if len(response) != 12 || binary.BigEndian.Uint16(response[:2]) != 0x1234 || response[2]&0x80 == 0 || response[3]&0x0f != 2 {
		t.Fatalf("unexpected SERVFAIL: %x", response)
	}
}

func TestRouterLoopbackQueryUsesWANEvenInProxyOnlyMode(t *testing.T) {
	upstream, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		packet := make([]byte, 512)
		n, peer, err := upstream.ReadFrom(packet)
		if err == nil {
			packet[2] = 0x81
			packet[3] = 0x80
			_, _ = upstream.WriteTo(packet[:n], peer)
		}
	}()
	s := &server{config: Config{
		Mode: "proxy-only", DefaultPolicy: "main",
		DirectURL: "udp://" + upstream.LocalAddr().String(),
		ProxyURL:  "https://1.1.1.1/dns-query",
	}, cache: make(map[string]*cacheEntry)}
	query := dnsQuery("example.com")
	response := s.resolve(context.Background(), query, netip.MustParseAddr("127.0.0.1"))
	if len(response) < 12 || response[3]&0x0f != 0 || s.directUse.Load() != 1 || s.proxyUse.Load() != 0 {
		t.Fatalf("router query did not use WAN: %x", response)
	}
}

func TestCacheRespectsTTLAndChangesTransactionID(t *testing.T) {
	query := dnsQuery("example.com")
	response := append([]byte(nil), query...)
	response[2] = 0x81
	response[3] = 0x80
	response[6] = 0
	response[7] = 1
	response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 10, 0, 4, 1, 2, 3, 4)
	s := &server{}
	s.remember("key", response)
	cached := s.cached("key", []byte{0x55, 0x66})
	if len(cached) != len(response) || cached[0] != 0x55 || cached[1] != 0x66 ||
		binary.BigEndian.Uint32(cached[len(query)+6:len(query)+10]) != 10 {
		t.Fatalf("unexpected cache hit: %x", cached)
	}
	s.cacheMu.Lock()
	entry := s.cache["key"]
	entry.stored = time.Now().Add(-4 * time.Second)
	s.cache["key"] = entry
	s.cacheMu.Unlock()
	cached = s.cached("key", []byte{0xaa, 0xbb})
	if binary.BigEndian.Uint32(cached[len(query)+6:len(query)+10]) > 6 {
		t.Fatalf("cached TTL was not reduced: %x", cached)
	}
	s.cacheMu.Lock()
	entry = s.cache["key"]
	entry.expires = time.Now().Add(-time.Second)
	s.cache["key"] = entry
	s.cacheMu.Unlock()
	if s.cached("key", []byte{1, 2}) != nil {
		t.Fatal("expired response returned")
	}
}

func TestDirectDomainMatcherUsesRouteRules(t *testing.T) {
	matcher, err := compileMatcher(Policy{DirectDomains: []string{"example.com"}, RUZoneMode: "both",
		DirectServiceExpressions: []string{"full:bank.test", "domain:service.test", "keyword:gov", "regexp:^custom[0-9]+\\.test$"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"example.com", "www.example.com", "bank.test", "sub.service.test",
		"x.gov.example", "custom12.test", "site.ru", "site.xn--p1ai"} {
		if !matcher.matches(name) {
			t.Errorf("direct domain missed: %s", name)
		}
	}
	for _, name := range []string{"notexample.com", "otherbank.test", "example.net", "ru.example"} {
		if matcher.matches(name) {
			t.Errorf("unwanted direct match: %s", name)
		}
	}
	s := &server{matchers: map[string]domainMatcher{"main": matcher}}
	if !s.matchesDirect("main", "site.ru") {
		t.Fatal("initial RU match missing")
	}
	if err := s.updatePolicies([]Policy{{ID: "main", DirectDomains: []string{"new.test"}}}); err != nil {
		t.Fatal(err)
	}
	if s.matchesDirect("main", "site.ru") || !s.matchesDirect("main", "new.test") {
		t.Fatal("hot DNS matcher update failed")
	}
}

func TestReusableDomainRulesOverrideLegacyDirectDNS(t *testing.T) {
	matcher, err := compileMatcher(Policy{DirectDomains: []string{"example.com"}, Rules: []Rule{
		{Action: "proxy", Domains: []string{"full:login.example.com"}},
		{Action: "direct", Domains: []string{"new.example.net"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if matcher.matches("login.example.com") || !matcher.matches("www.example.com") || !matcher.matches("new.example.net") {
		t.Fatal("DNS routing did not preserve ordered rule group precedence")
	}
}

func TestSelectedDeviceDNSPrecedesAllDeviceList(t *testing.T) {
	s := &server{config: Config{DefaultPolicy: "main"}, prefixes: []policyPrefix{
		{"office", netip.MustParsePrefix("192.168.1.20/32")},
		{"office", netip.MustParsePrefix("fd00::20/128")},
	}}
	for source, want := range map[string]string{
		"192.168.1.20": "office", "192.168.1.21": "main", "fd00::20": "office", "fd00::21": "main",
	} {
		if got := s.policyFor(netip.MustParseAddr(source)); got != want {
			t.Fatalf("source %s: got %s want %s", source, got, want)
		}
	}
}
