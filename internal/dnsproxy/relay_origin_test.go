package dnsproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/session"
)

func originRelay(t *testing.T, source, dest string, stream bool, body []byte) []byte {
	t.Helper()
	prefix := relayQuery(t, source, dest, stream, nil)
	stage := byte(1)
	if stream {
		stage = 2
	}
	origin := session.NewDNSOrigin(body, stage)
	tlv := origin.TLV()
	binary.BigEndian.PutUint16(prefix[14:16], uint16(len(prefix)-16+len(tlv)))
	packet := append(prefix, tlv[:]...)
	return append(packet, body...)
}

func TestDNSRelayOriginInteropAndLegacy(t *testing.T) {
	for _, source := range []string{"192.0.2.8:45678", "[2001:db8::8]:45678"} {
		dest := "203.0.113.53:53"
		if strings.HasPrefix(source, "[") {
			dest = "[2001:db8::53]:53"
		}
		for _, stream := range []bool{false, true} {
			for _, size := range []int{0, 1, 3, 11, 36, 4095, 4096, 4097, 8192, 8193} {
				q := make([]byte, size)
				copy(q, dnsQuery("test.example"))
				got, peer, transport, origin, ok := unwrapDNSRelayOrigin(originRelay(t, source, dest, stream, q))
				stage := uint8(1)
				wantTransport := ingressUDP
				if stream {
					stage = 2
					wantTransport = ingressTCP
				}
				if !ok || (size <= maxDNSMessage && (!origin.checked || !origin.match)) || (size > maxDNSMessage && (origin.checked || origin.skip != 1)) || origin.stage != stage || origin.destination.String() != dest || origin.size != uint16(size) || peer.String() != source || transport != wantTransport || !bytes.Equal(got, q) {
					t.Fatalf("origin interop failed: %s %v %d %+v", source, stream, size, origin)
				}
				_, _, _, legacy, ok := unwrapDNSRelayOrigin(relayQuery(t, source, dest, stream, q))
				if !ok || legacy.report() != nil {
					t.Fatal("legacy metadata was advertised as verified")
				}
			}
		}
	}
}

func TestDNSRelayOriginRejectsInvalidMetadata(t *testing.T) {
	q := dnsQuery("test.example")
	packet := originRelay(t, "192.0.2.8:45678", "203.0.113.53:53", false, q)
	for _, mutate := range []func([]byte){func(p []byte) { p[28] = 0xf0 }, func(p []byte) { p[30]-- }, func(p []byte) { p[31] = 2 }, func(p []byte) { p[32] = 2 }, func(p []byte) { p[15]-- }, func(p []byte) { p[12] = 0x20 }} {
		p := append([]byte(nil), packet...)
		mutate(p)
		if _, _, _, _, ok := unwrapDNSRelayOrigin(p); ok {
			t.Fatal("unsupported or inconsistent outer metadata accepted")
		}
	}
	for n := 16; n < 61; n++ {
		if _, _, _, _, ok := unwrapDNSRelayOrigin(packet[:n]); ok {
			t.Fatal("truncated metadata accepted")
		}
	}
}

func TestDNSRelayOriginDetectsCopyChangesAsDNSFailure(t *testing.T) {
	q := dnsQuery("ORIGIN-PRIVATE-CANARY.example.test")
	packet := originRelay(t, "192.0.2.8:45678", "203.0.113.53:53", false, q)
	packet[len(packet)-1] ^= 1
	body, peer, _, origin, ok := unwrapDNSRelayOrigin(packet)
	if !ok || origin.match {
		t.Fatal("changed query passed integrity check")
	}
	s := rejectedHeaderFixture(t)
	var response []byte
	s.resolveAsyncPeer(t.Context(), body, peer, ingressRelayUDP, func(p []byte) { response = p }, origin)
	if len(response) < 12 || response[3]&15 != 2 || s.failureRelay.Load() != 1 || s.dnsFailures.Load() != 1 || s.failureInvalidQuery.Load() != 0 || s.upstreamExchanges.Load() != 0 {
		t.Fatal("relay corruption was hidden as client malformed input")
	}
	reports := readRejectedHeaders(t, s)
	if len(reports) != 1 || reports[0].Origin == nil || reports[0].Origin.Integrity != "mismatch" || reports[0].Handling != "servfail_relay" {
		t.Fatal("missing corruption report")
	}
	raw, _ := json.Marshal(reports)
	if bytes.Contains(raw, []byte("CANARY")) || bytes.Contains(raw, []byte("digest")) || bytes.Contains(raw, []byte("checksum")) || bytes.Contains(raw, []byte("payload\"")) {
		t.Fatal("private payload or digest published")
	}
	// Even a corrupt incoming response must not start a response loop.
	body[2] |= 0x80
	response = []byte{1}
	s.resolveAsyncPeer(t.Context(), body, peer, ingressRelayUDP, func(p []byte) { response = p }, origin)
	if len(response) != 0 {
		t.Fatal("corrupt response received a response")
	}
}

func TestDNSRelayOriginMalformedInputRemainsInputFailure(t *testing.T) {
	q := dnsQuery("private.example.test")
	q[9] = 1
	body, peer, _, origin, ok := unwrapDNSRelayOrigin(originRelay(t, "192.0.2.8:45678", "203.0.113.53:53", false, q))
	if !ok || !origin.match {
		t.Fatal("original malformed input was called corruption")
	}
	s := rejectedHeaderFixture(t)
	s.resolveAsyncPeer(t.Context(), body, peer, ingressRelayUDP, func(p []byte) {
		if p[3]&15 != 1 {
			t.Fatal("missing FORMERR")
		}
	}, origin)
	if s.failureInvalidQuery.Load() != 1 || s.dnsFailures.Load() != 0 {
		t.Fatal("client format failure counted as DNS processing error")
	}
	reports := readRejectedHeaders(t, s)
	if len(reports) != 1 || reports[0].Origin.Stage != "xray-udp-buffer" || reports[0].Origin.Integrity != "match" || reports[0].Origin.Authorities == nil || *reports[0].Origin.Authorities != 1 {
		t.Fatal("original numeric header missing")
	}
}

func TestDNSRelayOriginMissingFieldsAreNotReportedAsZero(t *testing.T) {
	for _, size := range []int{0, 1, 3, 5, 7, 9, 11} {
		q := dnsQuery("test.example")[:size]
		_, _, _, origin, ok := unwrapDNSRelayOrigin(originRelay(t, "192.0.2.8:45678", "203.0.113.53:53", true, q))
		if !ok {
			t.Fatal("valid short payload rejected")
		}
		report := origin.report()
		fields := []*uint16{report.Flags, report.Questions, report.Answers, report.Authorities, report.Additionals}
		for i, field := range fields {
			if (field != nil) != (size >= 4+2*i) {
				t.Fatal("missing numeric field reported as a real zero")
			}
		}
	}
}

func TestDNSRelayOriginReceiveBoundsDoNotCreateFalseCorruption(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(cacheableDoHReply(dnsQuery("limit.example.test"), 42))
	}))
	defer upstream.Close()
	s, address, _ := listenerFixture(t, 0, 8*dnsRequestMemory, func(s *server) {
		s.config.TrustLocalRelay = true
		s.proxy = upstream.Client()
		s.config.ProxyURL = upstream.URL + "/dns-query"
	})
	conn, err := net.Dial("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, source := range []string{"192.0.2.8:45678", "[2001:db8::8]:45678"} {
		dest := "203.0.113.53:53"
		if strings.HasPrefix(source, "[") {
			dest = "[2001:db8::53]:53"
		}
		for _, size := range []int{4095, 4096, 4097, 8192, 8193, 8194, 9000, 65400} {
			q := make([]byte, size)
			copy(q, dnsQuery("limit.example.test"))
			conn.SetDeadline(time.Now().Add(time.Second))
			_, err = conn.Write(originRelay(t, source, dest, true, q))
			if err != nil {
				t.Fatal(err)
			}
			var response [8192]byte
			n, err := conn.Read(response[:])
			if err != nil {
				t.Fatal(err)
			}
			want := byte(0)
			if size > maxDNSMessage {
				want = 5
			}
			if n < 12 || response[3]&15 != want || s.failureRelay.Load() != 0 || s.invalidQueryReasons[queryRelayHeader].Load() != 0 {
				t.Fatalf("observer truncated payload: size=%d rcode=%d", size, response[3]&15)
			}
		}
	}
	if s.dnsFailures.Load() != 0 || s.invalidQueryReasons[queryTooLarge].Load() != 12 {
		t.Fatal("size policy was treated as transport corruption")
	}
}

func TestDNSRelayOriginResponseMutationNeverGetsAReply(t *testing.T) {
	original := dnsQuery("response.example.test")
	original[2] |= 0x80
	packet := originRelay(t, "192.0.2.8:45678", "203.0.113.53:53", false, original)
	_, peer, _, origin, ok := unwrapDNSRelayOrigin(packet)
	if !ok {
		t.Fatal("bad fixture")
	}
	origin.match = false
	for _, size := range []int{0, 1, 3, len(original)} {
		delivered := append([]byte(nil), original[:size]...)
		if size >= 3 {
			delivered[2] &= 0x7f
		}
		s := rejectedHeaderFixture(t)
		response := []byte{1}
		s.resolveAsyncPeer(t.Context(), delivered, peer, ingressRelayUDP, func(p []byte) { response = p }, origin)
		if len(response) != 0 || s.failureRelay.Load() != 1 {
			t.Fatal("mutated DNS response received a reply")
		}
		reports := readRejectedHeaders(t, s)
		if reports[0].Handling != "ignore_corrupt_response" {
			t.Fatal("reported handling disagrees with ignored original response")
		}
	}
}

func TestDNSRelayOriginOversizedResponseMutationNeverGetsAReply(t *testing.T) {
	for _, size := range []int{4097, 8192} {
		original := make([]byte, size)
		copy(original, dnsQuery("response.example.test"))
		original[2] |= 0x80
		packet := originRelay(t, "192.0.2.8:45678", "203.0.113.53:53", true, original)
		packet[61+2] &= 0x7f
		delivered, peer, _, origin, ok := unwrapDNSRelayOrigin(packet)
		if !ok || origin.checked {
			t.Fatal("oversize fixture did not skip integrity comparison")
		}
		s := rejectedHeaderFixture(t)
		response := []byte{1}
		s.resolveAsyncPeer(t.Context(), delivered, peer, ingressRelayTCP, func(p []byte) { response = p }, origin)
		if len(response) != 0 || s.failureRelay.Load() != 0 {
			t.Fatal("oversized original response received REFUSED")
		}
		reports := readRejectedHeaders(t, s)
		if reports[0].Handling != "ignore_size_response" {
			t.Fatal("reported handling disagrees with oversized response ignore")
		}
	}
}

func TestDNSRelayOriginNormalMismatchNormal(t *testing.T) {
	s, address, _ := listenerFixture(t, 0, 16*dnsRequestMemory, func(s *server) { s.config.TrustLocalRelay = true })
	conn, err := net.Dial("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, code := range []byte{0, 2, 0} {
		q := dnsQuery("normal.example.test")
		p := originRelay(t, "192.0.2.8:45678", "203.0.113.53:53", false, q)
		if code == 2 {
			p[len(p)-1] ^= 1
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write(p); err != nil {
			t.Fatal(err)
		}
		var reply [4096]byte
		n, err := conn.Read(reply[:])
		if err != nil || n < 12 || reply[3]&15 != code {
			t.Fatalf("normal/mismatch/normal failed: %v", err)
		}
	}
	if s.failureRelay.Load() != 1 || s.failureInvalidQuery.Load() != 0 || s.dnsFailures.Load() != 1 || s.upstreamExchanges.Load() != 1 {
		t.Fatal("corruption affected normal requests or hid its failure")
	}
}

func TestDNSRelayOriginConcurrentClientsDoNotInventMismatch(t *testing.T) {
	s, address, _ := listenerFixture(t, 0, 64*dnsRequestMemory, func(s *server) { s.config.TrustLocalRelay = true })
	var group sync.WaitGroup
	for i := range 16 {
		group.Go(func() {
			conn, err := net.Dial("udp4", address)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			for n := range 4 {
				q := dnsQuery(fmt.Sprintf("client-%d.example.test", i))
				binary.BigEndian.PutUint16(q[:2], uint16(i*4+n))
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := conn.Write(originRelay(t, fmt.Sprintf("192.0.2.%d:12345", i+2), "203.0.113.53:53", false, q)); err != nil {
					t.Error(err)
					return
				}
				var reply [4096]byte
				size, err := conn.Read(reply[:])
				if err != nil {
					t.Error(err)
					return
				}
				if size < 12 || reply[3]&15 != 0 || !matchingQuestion(q, reply[:size]) {
					t.Error("concurrent query changed or failed")
					return
				}
			}
		})
	}
	group.Wait()
	if s.failureRelay.Load() != 0 || s.failureInvalidQuery.Load() != 0 || s.dnsFailures.Load() != 0 {
		t.Fatal("observer invented a concurrent failure")
	}
}

type forgedRelayConn struct {
	packet []byte
	read   bool
	reply  []byte
}

func (c *forgedRelayConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if c.read {
		return 0, nil, io.EOF
	}
	c.read = true
	return copy(b, c.packet), &net.UDPAddr{IP: net.ParseIP("192.0.2.99"), Port: 12345}, nil
}
func (c *forgedRelayConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.reply = append([]byte(nil), p...)
	return len(p), nil
}
func (*forgedRelayConn) Close() error                     { return nil }
func (*forgedRelayConn) LocalAddr() net.Addr              { return &net.UDPAddr{IP: net.IPv4zero, Port: 1053} }
func (*forgedRelayConn) SetDeadline(time.Time) error      { return nil }
func (*forgedRelayConn) SetReadDeadline(time.Time) error  { return nil }
func (*forgedRelayConn) SetWriteDeadline(time.Time) error { return nil }

func TestDNSRelayOriginForgedLANMetadataIsNeverTrusted(t *testing.T) {
	q := dnsQuery("forged.example.test")
	conn := &forgedRelayConn{packet: originRelay(t, "192.0.2.8:45678", "203.0.113.53:53", false, q)}
	s := rejectedHeaderFixture(t)
	s.config.TrustLocalRelay = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.serveUDP(ctx, conn)
	if len(conn.reply) < 12 || conn.reply[3]&15 != 1 || s.upstreamExchanges.Load() != 0 || s.failureInvalidQuery.Load() != 1 || s.failureRelay.Load() != 0 {
		t.Fatal("forged LAN metadata reached managed DNS")
	}
	reports := readRejectedHeaders(t, s)
	if len(reports) != 1 || reports[0].Source != "192.0.2.99" || reports[0].Origin != nil || reports[0].Delivery != "listener" {
		t.Fatal("LAN peer supplied its own origin/identity")
	}
}
