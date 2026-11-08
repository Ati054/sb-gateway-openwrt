package dnsproxy

import (
	"net/netip"
	"testing"
)

func TestMalformedAdditionalRecordsNeverReachUpstream(t *testing.T) {
	for _, section := range []int{7, 9, 11} {
		s := rejectedHeaderFixture(t)
		q := dnsQuery("structural.example.test")
		q[section] = 1
		// Header declares an RR but its bytes are absent. A complete question
		// alone is insufficient to authorize a request to an upstream worker.
		response := s.resolve(t.Context(), q, netip.MustParseAddr("192.0.2.8"))
		if response[3]&15 != 1 || s.upstreamExchanges.Load() != 0 || s.dnsFailures.Load() != 0 || s.invalidQueryReasons[queryInvalidSections].Load() != 1 {
			t.Fatal("malformed section reached a resolver or was counted as a DNS processing error")
		}
	}
}
