package dnsproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestWANDNSUsesOnlyActiveUplinksAndRejectsLocalLoops(t *testing.T) {
	body := []byte(`{"interface":[
 {"interface":"lan","up":true,"proto":"static","l3_device":"br-lan","dns-server":["192.0.2.9"],"route":[]},
 {"interface":"vpn","up":true,"proto":"wireguard","l3_device":"wg0","dns-server":["192.0.2.10"],"route":[{"target":"0.0.0.0","mask":0}]},
 {"interface":"old","up":false,"proto":"dhcp","dns-server":["192.0.2.11"],"route":[{"target":"0.0.0.0","mask":0}]},
 {"interface":"wan","up":true,"proto":"dhcp","l3_device":"eth1","dns-server":["127.0.0.1","::1","0.0.0.0","224.0.0.1","192.0.2.1","192.0.2.53","192.0.2.54","192.0.2.53"],"route":[{"target":"0.0.0.0","mask":0,"metric":10}]},
 {"interface":"wan6","up":true,"proto":"dhcpv6","l3_device":"eth1","dns-server":["fe80::1","2001:db8::53"],"route":[{"target":"::","mask":0,"metric":20}]}
]}`)
	servers, err := parseWANDNS(body, map[netip.Addr]bool{netip.MustParseAddr("192.0.2.1"): true})
	if err != nil {
		t.Fatal(err)
	}
	if got := servers; len(got) != 4 || got[0].Address != "192.0.2.53:53" || got[1].Address != "192.0.2.54:53" || got[2].Address != "[fe80::1%eth1]:53" || got[3].Address != "[2001:db8::53]:53" || got[0].Interface != "eth1" {
		t.Fatalf("wrong WAN DNS: %v", got)
	}
	for _, data := range [][]byte{[]byte(`{"interface":[]}`), []byte(`{"interface":[{"up":true,"proto":"none","l3_device":"tun0","dns-server":["192.0.2.53"],"route":[{"target":"0.0.0.0","mask":0}]}]}`), []byte(`not json`)} {
		if _, err := parseWANDNS(data, nil); err == nil {
			t.Fatal("missing WAN or invalid data accepted")
		}
	}
}

func TestWANRefreshChangesCacheIdentityWithoutDiscardingPreferredServer(t *testing.T) {
	ip := netip.MustParseAddr("192.0.2.53")
	state := &wanDNSState{lookup: func(context.Context) ([]Endpoint, error) {
		return []Endpoint{{Address: net.JoinHostPort(ip.String(), "53")}, {Address: "192.0.2.54:53"}}, nil
	}}
	state.refresh(t.Context())
	first := state.current.Load()
	first.preferred.Store(1)
	state.refresh(t.Context())
	if state.current.Load() != first || state.current.Load().preferred.Load() != 1 {
		t.Fatal("unchanged DHCP data discarded resolver preference")
	}
	ip = netip.MustParseAddr("192.0.2.55")
	state.refresh(t.Context())
	if state.current.Load().key == first.key || state.current.Load().preferred.Load() != 0 {
		t.Fatal("DNS cache identity survived a WAN resolver change")
	}
}

func TestWANAddressesRefreshOnEventsWithoutRestart(t *testing.T) {
	var version atomic.Int32
	called := make(chan struct{}, 4)
	state := &wanDNSState{lookup: func(context.Context) ([]Endpoint, error) {
		called <- struct{}{}
		if version.Load() == 0 {
			return []Endpoint{{Address: "192.0.2.53:53"}}, nil
		}
		return []Endpoint{{Address: "192.0.2.54:53"}}, nil
	}}
	state.refresh(t.Context())
	<-called
	first := state.current.Load()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	changes := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { defer close(done); state.watch(ctx, changes) }()
	version.Store(1)
	changes <- struct{}{}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("interface event did not refresh DNS immediately")
	}
	// The lookup reports entry before publishing its immutable snapshot.
	deadline := time.Now().Add(time.Second)
	for state.current.Load() == first && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if state.current.Load() == first {
		t.Fatal("WAN DNS cache generation did not change")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WAN event listener did not stop")
	}
}

func TestWANOneEventRecoversTransientStatusFailureWithoutPolling(t *testing.T) {
	var calls atomic.Int32
	state := &wanDNSState{lookup: func(context.Context) ([]Endpoint, error) {
		n := calls.Add(1)
		if n == 2 {
			return nil, errors.New("temporary ubus failure")
		}
		address := "192.0.2.53:53"
		if n > 2 {
			address = "192.0.2.54:53"
		}
		return []Endpoint{{Address: address}}, nil
	}}
	state.refresh(t.Context())
	first := state.current.Load()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	changes := make(chan struct{}, 1)
	go state.watch(ctx, changes)
	changes <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		if calls.Load() == 2 && state.current.Load() != first {
			t.Fatal("transient read discarded working DNS")
		}
		time.Sleep(time.Millisecond)
	}
	for state.current.Load() == first && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if state.current.Load() == first {
		t.Fatal("single event never recovered")
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 3 {
		t.Fatalf("stable WAN was polled: %d", calls.Load())
	}
}

func TestWANIdentityIncludesUplinkAndSourceRoute(t *testing.T) {
	state := &wanDNSState{}
	device, source, gateway := "eth1", "192.0.2.2", "192.0.2.1"
	state.lookup = func(context.Context) ([]Endpoint, error) {
		body := []byte(`{"interface":[{"interface":"wan","up":true,"proto":"dhcp","l3_device":"` + device + `","ipv4-address":[{"address":"` + source + `","mask":24}],"dns-server":["192.0.2.53"],"route":[{"target":"0.0.0.0","mask":0,"nexthop":"` + gateway + `"}]}]}`)
		return parseWANDNS(body, nil)
	}
	state.refresh(t.Context())
	for _, change := range []func(){func() { device = "eth2" }, func() { source = "192.0.2.3" }, func() { gateway = "192.0.2.254" }} {
		previous := state.current.Load()
		change()
		state.refresh(t.Context())
		if state.current.Load().key == previous.key {
			t.Fatal("uplink change reused old DNS cache identity")
		}
	}
	body := []byte(`{"interface":[{"interface":"wan","up":true,"proto":"dhcp","l3_device":"eth1","dns-server":["192.0.2.53"],"route":[{"target":"0.0.0.0","mask":0}]},{"interface":"wan2","up":true,"proto":"dhcp","l3_device":"eth2","dns-server":["192.0.2.53"],"route":[{"target":"0.0.0.0","mask":0}]}]}`)
	servers, err := parseWANDNS(body, nil)
	if err != nil || len(servers) != 2 || servers[0].Interface == servers[1].Interface {
		t.Fatal("same DNS IP on separate WANs was merged")
	}
}

func wanUDPFixture(t *testing.T, answer bool) (Endpoint, *atomic.Int32) {
	t.Helper()
	socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	calls := &atomic.Int32{}
	go func() {
		var b [4096]byte
		for {
			n, a, err := socket.ReadFrom(b[:])
			if err != nil {
				return
			}
			calls.Add(1)
			if answer {
				_, _ = socket.WriteTo(cacheableDoHReply(b[:n], 42), a)
			}
		}
	}()
	return Endpoint{Protocol: "udp", Address: socket.LocalAddr().String()}, calls
}

func TestWANFailoverUsesSharedDeadlineAndKeepsWorkingResolver(t *testing.T) {
	slow, slowCalls := wanUDPFixture(t, false)
	healthy, healthyCalls := wanUDPFixture(t, true)
	snapshot := &wanDNSSnapshot{servers: []Endpoint{slow, healthy}}
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	if _, err := exchangeWANDNS(ctx, snapshot, dnsQuery("first.example")); err != nil {
		t.Fatal(err)
	}
	if snapshot.preferred.Load() != 1 {
		t.Fatal("healthy ISP resolver was not retained")
	}
	failedCalls := slowCalls.Load()
	if _, err := exchangeWANDNS(t.Context(), snapshot, dnsQuery("second.example")); err != nil {
		t.Fatal(err)
	}
	if failedCalls < 1 || failedCalls > 2 || slowCalls.Load() != failedCalls || healthyCalls.Load() != 2 {
		t.Fatalf("kept retrying the failed resolver: %d/%d", slowCalls.Load(), healthyCalls.Load())
	}
}

func TestWANResolverChangeDoesNotReuseOldDNSCache(t *testing.T) {
	first, firstCalls := wanUDPFixture(t, true)
	second, secondCalls := wanUDPFixture(t, true)
	w := &wanDNSState{}
	w.current.Store(&wanDNSSnapshot{servers: []Endpoint{first}, key: "first"})
	s := &server{config: Config{DefaultPolicy: "main", DirectURL: WANResolverURL}, wanDNS: w, cacheBudget: 1 << 20}
	query := dnsQuery("cache.example")
	source := netip.MustParseAddr("127.0.0.1")
	for range 2 {
		if r := s.resolve(t.Context(), query, source); r[3]&15 != 0 {
			t.Fatal("WAN DNS failed")
		}
	}
	w.current.Store(&wanDNSSnapshot{servers: []Endpoint{second}, key: "second"})
	if r := s.resolve(t.Context(), query, source); r[3]&15 != 0 {
		t.Fatal("replacement WAN DNS failed")
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 || s.cacheHits.Load() != 1 {
		t.Fatal("old WAN DNS cache crossed the resolver change")
	}
}

func TestWANDNSUnavailableDoesNotFallBackToAnotherProvider(t *testing.T) {
	w := &wanDNSState{}
	w.current.Store(&wanDNSSnapshot{key: "unavailable"})
	s := &server{config: Config{DefaultPolicy: "main", DirectURL: WANResolverURL}, wanDNS: w}
	if r := s.resolve(t.Context(), dnsQuery("test.example"), netip.MustParseAddr("127.0.0.1")); r[3]&15 != 2 || s.failureEndpoint.Load() != 1 || s.upstreamExchanges.Load() != 0 {
		t.Fatal("missing ISP DNS opened an unintended fallback")
	}
	if _, err := ParseEndpoint(WANResolverURL, false); err == nil {
		t.Fatal("ISP resolver was allowed on a proxy route")
	}
	var output wanStatusOutput
	if _, err := output.Write(make([]byte, 256*1024+1)); err == nil {
		t.Fatal("unbounded netifd status accepted")
	}
	if _, err := json.Marshal(s.config); err != nil {
		t.Fatal(err)
	}
}
