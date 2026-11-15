package dnsproxy

import (
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestDoHRejectsTruncatedDeclaredResponseSections(t *testing.T) {
	for _, section := range []string{"answer", "authority", "additional"} {
		t.Run(section, func(t *testing.T) {
			var calls atomic.Int32
			s, publish := managedFixture(t, func(w http.ResponseWriter, r *http.Request) {
				q, _ := io.ReadAll(r.Body)
				answer := cacheableDoHReply(q, 42)
				if calls.Add(1) == 1 {
					switch section {
					case "answer":
						answer = answer[:len(answer)-1]
					case "authority":
						binary.BigEndian.PutUint16(answer[8:10], 1)
					case "additional":
						binary.BigEndian.PutUint16(answer[10:12], 1)
					}
				}
				w.Header().Set("Content-Type", "application/dns-message")
				w.Write(answer)
			})
			publish("resproxy-main", "working")
			q := dnsQuery("section.example")
			peer := netip.MustParseAddr("192.0.2.8")
			first := s.resolve(t.Context(), q, peer)
			if len(first) < 12 || first[3]&15 != 2 || s.upstreamInvalid.Load() != 1 {
				t.Fatal("malformed upstream section was accepted instead of SERVFAIL")
			}
			for range 2 {
				if reply := s.resolve(t.Context(), q, peer); !matchingQuestion(q, reply) || reply[3]&15 != 0 {
					t.Fatal("healthy answer failed after malformed reply")
				}
			}
			if calls.Load() != 2 || s.cacheHits.Load() != 1 {
				t.Fatal("malformed reply polluted the shared cache")
			}
		})
	}
}

func TestDoHDoesNotReplayAXFRorIXFR(t *testing.T) {
	for _, kind := range []uint16{251, 252} {
		var calls atomic.Int32
		client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, io.EOF })}
		q := dnsQuery("zone.example")
		binary.BigEndian.PutUint16(q[len(q)-4:len(q)-2], kind)
		_, err := exchangeDoH(t.Context(), client, "https://resolver.example/dns-query", q)
		if !errors.Is(err, io.EOF) || calls.Load() != 1 {
			t.Fatalf("transfer request replayed after EOF: type=%d calls=%d error=%v", kind, calls.Load(), err)
		}
	}
}

func TestAutomaticDNSFailureReportsBothWireAttempts(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.udpAnswers.Store(true)
	f.wrongID.Store(true)
	_, err := exchangeAutomaticDNS(t.Context(), f.endpoint, dnsQuery("attempts.example"), nil)
	var detail *upstreamExchangeError
	if !errors.Is(err, errDNSResponseMismatch) || !errors.As(err, &detail) || f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 1 {
		t.Fatalf("two failed wire attempts were not observed: err=%v udp=%d tcp=%d", err, f.udpCalls.Load(), f.tcpCalls.Load())
	}
	if detail.attempts != 2 {
		t.Fatalf("two wire attempts reported as %d", detail.attempts)
	}
}
