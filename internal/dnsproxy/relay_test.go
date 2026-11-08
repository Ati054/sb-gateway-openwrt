package dnsproxy

import (
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/dnsroute"
	"github.com/pires/go-proxyproto"
)

func relayQuery(t *testing.T, source, dest string, stream bool, query []byte) []byte {
	t.Helper()
	src, dst := netip.MustParseAddrPort(source), netip.MustParseAddrPort(dest)
	var a, b net.Addr = net.UDPAddrFromAddrPort(src), net.UDPAddrFromAddrPort(dst)
	if stream {
		a, b = net.TCPAddrFromAddrPort(src), net.TCPAddrFromAddrPort(dst)
	}
	header, err := proxyproto.HeaderProxyFromAddrs(2, a, b).Format()
	if err != nil {
		t.Fatal(err)
	}
	return append(header, query...)
}

func TestLocalDNSRelayPreservesClientAndOriginalTransport(t *testing.T) {
	for _, source := range []string{"192.0.2.8:45678", "[2001:db8::8]:45678"} {
		for _, stream := range []bool{false, true} {
			t.Run(source+"/"+map[bool]string{false: "udp", true: "tcp"}[stream], func(t *testing.T) {
				s, address, _ := listenerFixture(t, 0, 8*dnsRequestMemory, func(s *server) {
					s.config.TrustLocalRelay = true
					s.config.StatusPath = t.TempDir() + "/dns-status.json"
				})
				conn, err := net.Dial("udp4", address)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				q := dnsQuery("relay.example.test")
				dest := "203.0.113.53:53"
				if netip.MustParseAddrPort(source).Addr().Is6() {
					dest = "[2001:db8::53]:53"
				}
				for _, broken := range []bool{false, true} {
					if broken {
						q[5] = 2
					}
					if _, err := conn.Write(relayQuery(t, source, dest, stream, q)); err != nil {
						t.Fatal(err)
					}
					var response [4096]byte
					n, err := conn.Read(response[:])
					if err != nil {
						t.Fatal(err)
					}
					want := byte(0)
					if broken {
						want = 1
					}
					if n < 12 || response[3]&15 != want || binary.BigEndian.Uint16(response[:2]) != 0x1234 {
						t.Fatal("local relay corrupted DNS framing or response")
					}
				}
				if s.proxyUse.Load() != 1 || s.directUse.Load() != 0 || s.upstreamExchanges.Load() != 1 || s.dnsFailures.Load() != 0 || s.failureInvalidQuery.Load() != 1 {
					t.Fatal("relayed client became a loopback WAN query or malformed input reached upstream")
				}
				entries := readRejectedHeaders(t, s)
				wantTransport := "udp"
				if stream {
					wantTransport = "tcp"
				}
				if len(entries) != 1 || entries[0].Source != netip.MustParseAddrPort(source).Addr().String() || entries[0].SourcePort != 45678 || entries[0].Transport != wantTransport {
					t.Fatal("relay diagnostics lost original client identity")
				}
			})
		}
	}
}

func TestDNSRelayCannotBypassBlockedClientRoute(t *testing.T) {
	registry := dnsroute.New([]string{"resproxy-main"})
	core := dnsroute.Core{PID: 42, StartTicks: "1"}
	registry.SetCore(core)
	registry.Publisher(core)("resproxy-main", "block")
	s, address, _ := listenerFixture(t, 0, 8*dnsRequestMemory, func(s *server) { s.config.TrustLocalRelay = true; s.config.Routes = registry })
	conn, err := net.Dial("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(relayQuery(t, "192.0.2.8:45678", "203.0.113.53:53", false, dnsQuery("blocked.example.test"))); err != nil {
		t.Fatal(err)
	}
	var response [128]byte
	n, err := conn.Read(response[:])
	if err != nil {
		t.Fatal(err)
	}
	if n < 12 || response[3]&15 != 2 || s.upstreamExchanges.Load() != 0 || s.directUse.Load() != 0 || s.failureRouteBlocked.Load() != 1 || s.dnsFailures.Load() != 1 {
		t.Fatal("relayed source bypassed a blocked selector")
	}
}

func TestDNSRelayRejectsUntrustedAndMalformedEnvelopes(t *testing.T) {
	s, address, _ := listenerFixture(t, 0, 8*dnsRequestMemory)
	conn, err := net.Dial("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	p := relayQuery(t, "192.0.2.8:45678", "203.0.113.53:53", false, dnsQuery("relay.example.test"))
	if _, err := conn.Write(p); err != nil {
		t.Fatal(err)
	}
	var response [128]byte
	if _, err := conn.Read(response[:]); err != nil {
		t.Fatal(err)
	}
	if s.upstreamExchanges.Load() != 0 || s.failureInvalidQuery.Load() != 1 {
		t.Fatal("disabled relay accepted client-supplied metadata")
	}
	for _, mutate := range []func([]byte){func(p []byte) { p[12] = 0x20 }, func(p []byte) { p[15] = 255 }, func(p []byte) { p[13] = 0x13 }, func(p []byte) { p[24], p[25] = 0, 0 }} {
		q := append([]byte(nil), p...)
		mutate(q)
		if _, _, _, ok := unwrapDNSRelay(q); ok {
			t.Fatal("invalid local envelope accepted")
		}
	}
}
