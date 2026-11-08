package dnsproxy

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
	"unsafe"
)

func TestRejectedRecentSamplesKeepDifferentDevicesWithSameReason(t *testing.T) {
	s := rejectedHeaderFixture(t)
	for i := range 384 {
		peer := netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}), uint16(40000+i))
		s.rejectedHeaders.recordPeer([]byte{1}, peer, ingressUDP, queryShortHeader)
	}
	readRejectedHeaders(t, s)
	body, err := os.ReadFile(s.rejectedHeadersPath())
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		RecentCapacity int                    `json:"recent_capacity"`
		RecentSamples  []rejectedHeaderReport `json:"recent_samples"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if report.RecentCapacity != 256 || len(report.RecentSamples) != 256 {
		t.Fatal("recent diagnostics are unbounded or lost distinct devices")
	}
	for i, entry := range report.RecentSamples {
		want := i + 128
		if entry.Sequence != uint64(want+1) || entry.SourcePort != uint16(40000+want) || entry.Source != netip.AddrFrom4([4]byte{192, 0, 2, byte(want)}).String() {
			t.Fatalf("recent events were reordered or mixed: %+v", entry)
		}
	}
	if s.failures.Load() != 0 {
		t.Fatal("diagnostic recording changed DNS accounting")
	}
	// The fixed 256-entry ring also retains the bounded numeric origin stamp.
	// This allowance covers diagnostics only; DNS admission/cache budgets do not change.
	if unsafe.Sizeof(rejectedHeaders{}) > 48<<10 {
		t.Fatal("retained rejection diagnostics exceed their fixed memory allowance")
	}
}

func TestRejectedProtocolHintSurvivesFollowingMalformedBurst(t *testing.T) {
	s := rejectedHeaderFixture(t)
	packet := make([]byte, 1280)
	packet[0], packet[4], packet[5], packet[14] = 0xc3, 1, 8, 8
	s.resolveAsyncPeer(t.Context(), packet, netip.MustParseAddrPort("192.0.2.8:45678"), ingressUDP, func([]byte) {})
	for range 1000 {
		s.rejectedHeaders.recordPeer([]byte{1}, netip.MustParseAddrPort("192.0.2.9:56789"), ingressUDP, queryShortHeader)
	}
	readRejectedHeaders(t, s)
	body, err := os.ReadFile(s.rejectedHeadersPath())
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Hints []rejectedHeaderReport `json:"wire_hint_samples"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Hints) != 1 || report.Hints[0].Sequence != 1 || report.Hints[0].SourcePort != 45678 || report.Hints[0].WireHint != "possible-quic-v1-long-header" {
		t.Fatal("a subsequent malformed burst erased the only protocol hint")
	}
}

func TestRejectedWireHintIsConservativeAndTransportSpecific(t *testing.T) {
	packet := make([]byte, 1280)
	packet[0] = 0xc3
	binary.BigEndian.PutUint32(packet[1:5], 1)
	packet[5], packet[14] = 8, 8
	for _, tc := range []struct {
		name    string
		mutate  func([]byte)
		ingress dnsIngress
		want    uint8
	}{
		{"v1", func([]byte) {}, ingressUDP, 1},
		{"v2", func(p []byte) { binary.BigEndian.PutUint32(p[1:5], 0x6b3343cf) }, ingressUDP, 2},
		{"tcp", func([]byte) {}, ingressTCP, 0},
		{"internal", func([]byte) {}, ingressInternal, 0},
		{"unknown-version", func(p []byte) { p[4] = 9 }, ingressUDP, 0},
		{"fixed-bit-unset", func(p []byte) { p[0] = 0x83 }, ingressUDP, 0},
		{"short-header", func(p []byte) { p[0] = 0x43 }, ingressUDP, 0},
		{"dcid-too-long", func(p []byte) { p[5] = 21 }, ingressUDP, 0},
		{"scid-too-long", func(p []byte) { p[14] = 21 }, ingressUDP, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := append([]byte(nil), packet...)
			tc.mutate(p)
			if got := rejectedWireHint(p, tc.ingress); got != tc.want {
				t.Fatalf("hint=%d want=%d", got, tc.want)
			}
		})
	}
	for _, size := range []int{0, 1, 4, 5, 12, 1199} {
		if rejectedWireHint(packet[:size], ingressUDP) != 0 {
			t.Fatal("identified truncated or small data as a QUIC candidate")
		}
	}
	s := rejectedHeaderFixture(t)
	peer := netip.MustParseAddrPort("[2001:db8::8]:45678")
	s.resolveAsyncPeer(t.Context(), packet, peer, ingressUDP, func([]byte) {})
	entries := readRejectedHeaders(t, s)
	if len(entries) != 1 || entries[0].SourcePort != 45678 || entries[0].WireHint != "possible-quic-v1-long-header" {
		t.Fatal("lost source endpoint or incorrectly promoted hint to protocol identification")
	}
	if s.upstreamExchanges.Load() != 0 || s.failureInvalidQuery.Load() != 1 {
		t.Fatal("wire hint changed rejection or upstream behavior")
	}
}

func TestRejectedTCPRetainsRealSourcePort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := rejectedHeaderFixture(t)
	done := make(chan struct{})
	go func() { defer close(done); s.handleTCP(t.Context(), conn) }()
	t.Cleanup(func() { client.Close(); conn.Close(); <-done })
	client.SetDeadline(time.Now().Add(5 * time.Second))
	p := dnsQuery("private-port-canary.example.test")
	p[5] = 2
	frame := append([]byte{byte(len(p) >> 8), byte(len(p))}, p...)
	if _, err := client.Write(frame); err != nil {
		t.Fatal(err)
	}
	var response [14]byte
	if _, err := io.ReadFull(client, response[:]); err != nil {
		t.Fatal(err)
	}
	entries := readRejectedHeaders(t, s)
	if len(entries) != 1 || entries[0].SourcePort != addrPort(client.LocalAddr()).Port() || entries[0].Source != "127.0.0.1" || entries[0].Transport != "tcp" {
		t.Fatal("TCP lost original client port")
	}
}
