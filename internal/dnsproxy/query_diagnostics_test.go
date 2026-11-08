package dnsproxy

import (
	"net/netip"
	"strings"
	"sync"
	"testing"
)

func TestRejectedQueryDiagnostics(t *testing.T) {
	header := func(count byte) []byte {
		return []byte{0x12, 0x34, 1, 0, 0, count, 0, 0, 0, 0, 0, 0}
	}
	brokenOPT := questionlessQuery(true)
	brokenOPT[22] = 99
	tests := []struct {
		name   string
		packet []byte
		reason queryFailure
	}{
		{"short header", []byte{0x12}, queryShortHeader},
		{"multiple questions", header(2), queryQuestionCount},
		{"truncated EDNS", brokenOPT, queryQuestionlessSections},
		{"missing question", header(1), queryTruncatedQuestion},
		{"compression pointer", append(header(1), 0xc0, 0x0c, 0, 1, 0, 1), queryCompressedName},
		{"reserved label encoding", append(header(1), 0x40, 0, 0, 1, 0, 1), queryInvalidLabel},
		{"truncated label", append(header(1), 10, 't', 'e', 's', 't'), queryTruncatedName},
		{"unterminated name", append(header(1), 4, 't', 'e', 's', 't'), queryTruncatedName},
		{"excessive label count", append(header(1), []byte(strings.Repeat("\x01a", 128)+"\x00\x00\x01\x00\x01")...), queryTooManyLabels},
		{"missing query type and class", append(header(1), 3, 't', 'e', 's', 0), queryTruncatedTypeClass},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := &server{}
			body := s.resolve(t.Context(), test.packet, netip.MustParseAddr("192.0.2.8"))
			if len(body) != 12 || body[3]&15 != 1 || body[5] != 0 {
				t.Fatal("invalid request was forwarded or its question was echoed")
			}
			if s.failureInvalidQuery.Load() != 1 || s.failures.Load() != 1 || s.activeQueries.Load() != 0 || s.upstreamExchanges.Load() != 0 {
				t.Fatal("request accounting changed or an invalid query reached upstream")
			}
			for reason := queryFailure(1); reason < queryFailureCount; reason++ {
				want := uint64(0)
				if reason == test.reason {
					want = 1
				}
				if got := s.invalidQueryReasons[reason].Load(); got != want {
					t.Fatalf("%s: got %d, want %d", reason, got, want)
				}
			}
		})
	}
}

func TestRejectedQueryDiagnosticsConcurrentAccounting(t *testing.T) {
	s := &server{}
	var group sync.WaitGroup
	for range 64 {
		group.Go(func() {
			s.resolve(t.Context(), []byte{1}, netip.MustParseAddr("192.0.2.8"))
		})
	}
	group.Wait()
	if s.failureInvalidQuery.Load() != 64 || s.invalidQueryReasons[queryShortHeader].Load() != 64 || s.activeQueries.Load() != 0 {
		t.Fatal("concurrent malformed queries lost diagnostic counts")
	}
}

func TestQueryDiagnosticsPreserveValidQuestionParsing(t *testing.T) {
	for _, query := range [][]byte{dnsQuery("MiXeD.Example.test"), questionlessQuery(false), questionlessQuery(true)} {
		_, reason := parseQuestionName(query)
		if reason != queryValid {
			t.Fatalf("valid request classified as %s", reason)
		}
	}
	query := dnsQuery("MiXeD.Example.test")
	// HTTPS (65), AAAA (28), and unknown future types use the same name routing.
	for _, kind := range []uint16{1, 28, 65, 65534} {
		query[len(query)-4], query[len(query)-3] = byte(kind>>8), byte(kind)
		name, reason := parseQuestionName(query)
		if name != "mixed.example.test" || reason != queryValid {
			t.Fatal("query type changed name routing")
		}
	}
}
