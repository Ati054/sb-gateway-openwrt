package dnsproxy

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestDNSRelayShortPayloadKeepsEnvelopeIdentity(t *testing.T) {
	for _, source := range []string{"192.0.2.8:45678", "[2001:db8::8]:45678"} {
		dest := "203.0.113.53:53"
		if netip.MustParseAddrPort(source).Addr().Is6() {
			dest = "[2001:db8::53]:53"
		}
		for _, stream := range []bool{false, true} {
			for _, size := range []int{0, 1, 3, 11} {
				body := dnsQuery("short.example.test")[:size]
				packet := relayQuery(t, source, dest, stream, body)
				prefixSize := len(packet) - len(body)
				if _, _, _, ok := unwrapDNSRelay(packet[:prefixSize-1]); ok {
					t.Fatal("truncated outer envelope was accepted")
				}
				got, peer, transport, ok := unwrapDNSRelay(packet)
				wantTransport := ingressUDP
				if stream {
					wantTransport = ingressTCP
				}
				if !ok || peer.String() != source || transport != wantTransport || !bytes.Equal(got, body) {
					t.Fatalf("valid outer envelope rejected: source=%s tcp=%v payload=%d", source, stream, size)
				}
			}
		}
	}
}

func TestDNSRelayShortPayloadReturnsFORMERRAndPreservesNextQuery(t *testing.T) {
	s, address, _ := listenerFixture(t, 0, 8*dnsRequestMemory, func(s *server) {
		s.config.TrustLocalRelay = true
		s.config.StatusPath = t.TempDir() + "/dns-status.json"
	})
	conn, err := net.Dial("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	source, dest := "192.0.2.8:45678", "203.0.113.53:53"
	exchange := func(body []byte, want byte) {
		t.Helper()
		conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write(relayQuery(t, source, dest, true, body)); err != nil {
			t.Fatal(err)
		}
		var reply [4096]byte
		n, err := conn.Read(reply[:])
		if err != nil {
			t.Fatalf("DNS relay left client waiting: %v", err)
		}
		if n < 12 || reply[3]&15 != want {
			t.Fatalf("unexpected DNS response: size=%d rcode=%d", n, reply[3]&15)
		}
		if len(body) >= 2 && binary.BigEndian.Uint16(reply[:2]) != binary.BigEndian.Uint16(body[:2]) {
			t.Fatal("FORMERR lost request identity")
		}
	}
	normal := dnsQuery("short.example.test")
	exchange(normal, 0)
	for _, size := range []int{0, 1, 3, 11} {
		exchange(normal[:size], 1)
	}
	exchange(normal, 0)
	if s.invalidQueryReasons[queryShortHeader].Load() != 4 || s.invalidQueryReasons[queryRelayHeader].Load() != 0 || s.dnsFailures.Load() != 0 || s.upstreamExchanges.Load() != 1 {
		t.Fatal("short DNS payload was misclassified or sent upstream")
	}
	reports := readRejectedHeaders(t, s)
	if len(reports) != 1 || reports[0].Source != "192.0.2.8" || reports[0].Transport != "tcp" || reports[0].Delivery != "tproxy" || reports[0].Handling != "formerr" {
		t.Fatalf("relay lost client or reply handling: %+v", reports)
	}
}
