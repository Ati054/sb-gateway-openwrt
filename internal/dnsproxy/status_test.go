package dnsproxy

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
)

func statusQuery(withQuestion bool) []byte {
	query := []byte{0x12, 0x34, 0x11, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if withQuestion {
		query = dnsQuery("status.example")
		query[2] = 0x11 // STATUS, recursion desired.
	}
	return query
}

func TestStatusQueryReturnsLocalNotImplemented(t *testing.T) {
	var calls atomic.Int32
	s, publish := managedFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		http.Error(w, "Unsupported status request", http.StatusBadRequest)
	})
	source := netip.MustParseAddr("192.0.2.8")
	for _, route := range []string{"working", "block"} {
		publish("resproxy-main", route)
		for _, ingress := range []dnsIngress{ingressUDP, ingressTCP, ingressRelayUDP, ingressRelayTCP} {
			for _, withQuestion := range []bool{false, true} {
				query := statusQuery(withQuestion)
				reply := s.resolveIngress(t.Context(), query, source, ingress)
				if len(reply) != 12 || !bytes.Equal(reply[:2], query[:2]) || reply[2] != 0x91 || reply[3] != 4 || !bytes.Equal(reply[4:], make([]byte, 8)) {
					t.Fatalf("%s/%d/question=%t: STATUS reply = %x, want header-only NOTIMP", route, ingress, withQuestion, reply)
				}
			}
		}
	}
	if calls.Load() != 0 || s.upstreamExchanges.Load() != 0 || s.dnsFailures.Load() != 0 || s.failureInvalidQuery.Load() != 0 || s.activeQueries.Load() != 0 {
		t.Fatal("STATUS reached upstream or counted as a failed lookup")
	}
	if s.localUse.Load() != 16 || s.unsupportedOpcode.Load() != 16 {
		t.Fatal("unsupported STATUS operations disappeared from diagnostics")
	}
}

func TestStatusQueryValidationPrecedesLocalReply(t *testing.T) {
	source := netip.MustParseAddr("192.0.2.8")
	t.Run("response", func(t *testing.T) {
		s := &server{}
		query := statusQuery(true)
		query[2] |= 0x80
		if reply := s.resolve(t.Context(), query, source); reply != nil || s.failureInvalidQuery.Load() != 1 {
			t.Fatal("answered an incoming STATUS response")
		}
	})
	t.Run("truncated", func(t *testing.T) {
		s := &server{}
		query := statusQuery(true)
		reply := s.resolve(t.Context(), query[:len(query)-1], source)
		if len(reply) < 12 || reply[3]&15 != 1 || s.failureInvalidQuery.Load() != 1 {
			t.Fatal("malformed STATUS skipped validation")
		}
	})
	t.Run("integrity", func(t *testing.T) {
		s := &server{}
		var reply []byte
		s.resolveAsyncPeer(t.Context(), statusQuery(true), netip.AddrPortFrom(source, 1234), ingressRelayUDP, func(body []byte) { reply = body }, relayOrigin{stage: 1, checked: true, match: false})
		if len(reply) < 12 || binary.BigEndian.Uint16(reply[2:4])&15 != 2 || s.failureRelay.Load() != 1 || s.unsupportedOpcode.Load() != 0 {
			t.Fatal("local STATUS answer bypassed relay integrity check")
		}
	})
}
