package dnsproxy

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func rejectedHeaderFixture(t *testing.T) *server {
	t.Helper()
	return &server{config: Config{StatusPath: filepath.Join(t.TempDir(), "dns-status.json")}}
}

func readRejectedHeaders(t *testing.T, s *server) []rejectedHeaderReport {
	t.Helper()
	if err := s.writeRejectedHeaders(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(s.rejectedHeadersPath())
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Capacity int                    `json:"capacity"`
		Samples  []rejectedHeaderReport `json:"samples"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if report.Capacity != int(queryFailureCount-1)*int(ingressCount) || len(report.Samples) > report.Capacity {
		t.Fatal("unbounded rejection diagnostics")
	}
	return report.Samples
}

func TestRejectedHeadersRetainOnlyNumericHeaderAndSource(t *testing.T) {
	s := rejectedHeaderFixture(t)
	packet := dnsQuery("PRIVATE-DOMAIN-CANARY.example.test")
	packet[5] = 2
	packet[6], packet[7], packet[8], packet[9], packet[10], packet[11] = 0, 3, 0, 4, 0, 5
	s.resolveAsyncIngress(t.Context(), packet, netip.MustParseAddr("2001:db8::20"), ingressTCP, func([]byte) {})
	// The retained diagnostic must not alias any part of the request buffer.
	for i := range packet {
		packet[i] = 0
	}
	entries := readRejectedHeaders(t, s)
	if len(entries) != 1 {
		t.Fatal("missing rejected header")
	}
	entry := entries[0]
	if entry.Reason != "question_count" || entry.Transport != "tcp" || entry.Source != "2001:db8::20" || entry.Opcode != 0 || entry.Flags != 256 || entry.Questions != 2 || entry.Answers != 3 || entry.Authorities != 4 || entry.Additionals != 5 || entry.HeaderBytes != 12 {
		t.Fatalf("incorrect diagnostic: %+v", entry)
	}
	body, _ := os.ReadFile(s.rejectedHeadersPath())
	for _, private := range [][]byte{[]byte("PRIVATE"), []byte("CANARY"), []byte("example.test"), []byte("transaction"), []byte("payload")} {
		if bytes.Contains(body, private) {
			t.Fatal("request contents leaked into diagnostics")
		}
	}
	info, err := os.Stat(s.rejectedHeadersPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("diagnostics are not private to the service owner")
	}
}

func TestRejectedHeadersBoundedAcrossSourcesAndConcurrentSnapshots(t *testing.T) {
	s := rejectedHeaderFixture(t)
	var group sync.WaitGroup
	group.Go(func() {
		for i := range 2000 {
			s.rejectedHeaders.record([]byte{1}, netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}), ingressUDP, queryShortHeader)
		}
	})
	group.Go(func() {
		for range 20 {
			if err := s.writeRejectedHeaders(); err != nil {
				t.Error(err)
				return
			}
		}
	})
	group.Wait()
	entries := readRejectedHeaders(t, s)
	if len(entries) != 1 || entries[0].Sequence != 2000 || entries[0].Source != "192.0.2.207" || entries[0].HeaderBytes != 1 || entries[0].Questions != 0 {
		t.Fatal("source churn grew diagnostics or lost the latest rejection")
	}
	before, _ := os.Stat(s.rejectedHeadersPath())
	if err := s.writeRejectedHeaders(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(s.rejectedHeadersPath())
	if !os.SameFile(before, after) {
		t.Fatal("unchanged diagnostics caused another file replacement")
	}
}

func TestRejectedHeadersInitialEmptyStateAndWriteRetry(t *testing.T) {
	s := rejectedHeaderFixture(t)
	if entries := readRejectedHeaders(t, s); len(entries) != 0 {
		t.Fatal("new service published stale rejected packets")
	}
	s.config.StatusPath = filepath.Join(t.TempDir(), "missing", "dns-status.json")
	s.rejectedHeaders.record([]byte{1}, netip.Addr{}, ingressUDP, queryShortHeader)
	if err := s.writeRejectedHeaders(); err == nil {
		t.Fatal("expected missing runtime directory error")
	}
	if err := os.Mkdir(filepath.Dir(s.config.StatusPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if entries := readRejectedHeaders(t, s); len(entries) != 1 || entries[0].Source != "unknown" {
		t.Fatal("failed diagnostic write suppressed the next successful write")
	}
}
