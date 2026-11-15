package dnsproxy

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func inverseQuery() []byte {
	// RFC 3425: IQUERY carries the sought value in its Answer section.
	query := []byte{0x12, 0x34, 0x09, 0, 0, 0, 0, 1, 0, 0, 0, 0}
	return append(query, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4, 192, 0, 2, 8)
}

func TestInverseQueryReturnsNotImplementedWithoutUpstream(t *testing.T) {
	var calls atomic.Int32
	s, publish := managedFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		http.Error(w, "Invalid query.", http.StatusBadRequest)
	})
	source := netip.MustParseAddr("192.0.2.8")
	for _, route := range []string{"working", "block"} {
		publish("resproxy-main", route)
		for _, ingress := range []dnsIngress{ingressUDP, ingressTCP, ingressRelayUDP, ingressRelayTCP} {
			query := inverseQuery()
			reply := s.resolveIngress(t.Context(), query, source, ingress)
			if len(reply) != 12 || !bytes.Equal(reply[:2], query[:2]) || reply[2] != 0x89 || reply[3] != 4 || !bytes.Equal(reply[4:], make([]byte, 8)) {
				t.Fatalf("%s/%d: IQUERY reply = %x, want header-only NOTIMP", route, ingress, reply)
			}
		}
	}
	if calls.Load() != 0 || s.upstreamExchanges.Load() != 0 || s.dnsFailures.Load() != 0 || s.failureInvalidQuery.Load() != 0 || s.activeQueries.Load() != 0 {
		t.Fatal("local unsupported operation contacted upstream or counted as a failed lookup")
	}
	if s.localUse.Load() != 8 || s.unsupportedOpcode.Load() != 8 {
		t.Fatal("unsupported operations disappeared from diagnostics")
	}
}

func TestUnsupportedOpcodeListenerKeepsNextLookupWorking(t *testing.T) {
	for _, operation := range []struct {
		name  string
		query []byte
	}{{"inverse", inverseQuery()}, {"status", statusQuery(true)}, {"status-empty", statusQuery(false)}} {
		for _, network := range []string{"udp4", "tcp4"} {
			t.Run(operation.name+"/"+network, func(t *testing.T) {
				s, address, _ := listenerFixture(t, 0, 2*1024*1024)
				conn, err := net.Dial(network, address)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				for index, query := range [][]byte{operation.query, dnsQuery("lookup.example")} {
					frame := query
					if network == "tcp4" {
						frame = append([]byte{byte(len(query) >> 8), byte(len(query))}, query...)
					}
					if _, err := conn.Write(frame); err != nil {
						t.Fatal(err)
					}
					var reply []byte
					if network == "tcp4" {
						var size [2]byte
						if _, err := io.ReadFull(conn, size[:]); err != nil {
							t.Fatal(err)
						}
						reply = make([]byte, binary.BigEndian.Uint16(size[:]))
						if _, err := io.ReadFull(conn, reply); err != nil {
							t.Fatal(err)
						}
					} else {
						var buffer [512]byte
						n, err := conn.Read(buffer[:])
						if err != nil {
							t.Fatal(err)
						}
						reply = buffer[:n]
					}
					if index == 0 {
						if len(reply) != 12 || reply[2] != operation.query[2]|0x80 || reply[3] != 4 || s.upstreamExchanges.Load() != 0 {
							t.Fatalf("unsupported operation reached upstream or lost NOTIMP: %x", reply)
						}
					} else if !matchingQuestion(query, reply) || reply[3]&15 != 0 {
						t.Fatal("local NOTIMP broke the following ordinary lookup")
					}
				}
				if s.upstreamExchanges.Load() != 1 || s.dnsFailures.Load() != 0 {
					t.Fatal("unexpected upstream accounting")
				}
			})
		}
	}
}

func TestInverseQueryValidationPrecedesLocalReply(t *testing.T) {
	source := netip.MustParseAddr("192.0.2.8")
	t.Run("response", func(t *testing.T) {
		s := &server{}
		query := inverseQuery()
		query[2] |= 0x80
		if reply := s.resolve(t.Context(), query, source); reply != nil || s.failureInvalidQuery.Load() != 1 {
			t.Fatal("answered an incoming response")
		}
	})
	t.Run("truncated", func(t *testing.T) {
		s := &server{}
		query := inverseQuery()
		reply := s.resolve(t.Context(), query[:len(query)-1], source)
		if reply[3]&15 != 1 || s.failureInvalidQuery.Load() != 1 {
			t.Fatal("malformed IQUERY skipped section validation")
		}
	})
	t.Run("integrity", func(t *testing.T) {
		s := &server{}
		var reply []byte
		s.resolveAsyncPeer(t.Context(), inverseQuery(), netip.AddrPortFrom(source, 1234), ingressRelayUDP, func(body []byte) { reply = body }, relayOrigin{stage: 1, checked: true, match: false})
		if len(reply) < 12 || binary.BigEndian.Uint16(reply[2:4])&15 != 2 || s.failureRelay.Load() != 1 {
			t.Fatal("local answer bypassed relay integrity check")
		}
	})
}
