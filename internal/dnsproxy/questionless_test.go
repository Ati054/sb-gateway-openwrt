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

func questionlessQuery(edns bool) []byte {
	query := []byte{0x12, 0x34, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if edns {
		query[11] = 1
		query = append(query, 0, 0, 41, 4, 208, 0, 0, 0, 0, 0, 12, 0, 10, 0, 8, 1, 2, 3, 4, 5, 6, 7, 8)
	}
	return query
}

func TestQuestionlessDNSMessagesUseAuthorizedRoute(t *testing.T) {
	// RFC 9619: a DNS middlebox must not reject QUERY/QDCOUNT=0 as malformed.
	var calls atomic.Int32
	s, publish := managedFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		body[2], body[3] = 0x81, 0x80
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(body)
	})
	source := netip.MustParseAddr("192.0.2.8")
	for _, edns := range []bool{false, true} {
		query := questionlessQuery(edns)
		publish("resproxy-main", "block")
		denied := s.resolve(t.Context(), query, source)
		if len(denied) != 12 || denied[3]&15 != 2 || binary.BigEndian.Uint16(denied[4:6]) != 0 || calls.Load() != 0 {
			t.Fatal("questionless request bypassed the blocked route or echoed OPT as a question")
		}
		publish("resproxy-main", "working")
		body := s.resolve(t.Context(), query, source)
		if len(body) != len(query) || body[3]&15 != 0 || !bytes.Equal(body[12:], query[12:]) || !matchingQuestion(query, body) {
			t.Fatal("valid questionless DNS request did not complete on the proxy path")
		}
		calls.Store(0)
	}
	if s.failureInvalidQuery.Load() != 0 || s.failureRouteBlocked.Load() != 2 {
		t.Fatal("valid control requests were counted as malformed")
	}
}

func TestQuestionlessMatchingRejectsMalformedAndUnrelatedMessages(t *testing.T) {
	query := questionlessQuery(true)
	response := append([]byte(nil), query...)
	response[2], response[3] = 0x81, 0x80
	if !matchingQuestion(query, response) {
		t.Fatal("valid EDNS-only response rejected")
	}
	for _, change := range []func([]byte){
		func(b []byte) { b[0]++ },
		func(b []byte) { b[2] &^= 0x80 },
		func(b []byte) { b[2] |= 0x08 },
		func(b []byte) { b[5] = 1 },
		func(b []byte) { b[22] = 99 }, // OPT data extends past the packet.
	} {
		bad := append([]byte(nil), response...)
		change(bad)
		if matchingQuestion(query, bad) {
			t.Fatal("unrelated or malformed questionless response accepted")
		}
	}
	broken := append([]byte(nil), query...)
	broken[22] = 99
	if _, ok := questionName(broken); ok {
		t.Fatal("truncated EDNS-only request accepted")
	}
}

func TestMalformedQuestionCountReturnsFormatError(t *testing.T) {
	s := &server{}
	query := dnsQuery("example.test")
	query[5] = 2
	body := s.resolve(t.Context(), query, netip.MustParseAddr("192.0.2.8"))
	if len(body) != 12 || body[3]&15 != 1 || body[5] != 0 || s.failureInvalidQuery.Load() != 1 {
		t.Fatal("malformed multi-question request must receive FORMERR, not transient SERVFAIL")
	}
}
