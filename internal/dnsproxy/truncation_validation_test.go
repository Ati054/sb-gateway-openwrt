package dnsproxy

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
)

func TestUDPTruncationRequiresMatchingReplyBeforeTCP(t *testing.T) {
	for _, mode := range []uint32{1, 2, 3, 4, 7, 8} {
		f := newAutomaticDNSFixture(t)
		f.udpTruncated.Store(true)
		f.udpTCVariant.Store(mode)
		f.udpAnswers.Store(true)
		ep := f.endpoint
		ep.Protocol = "udp"
		_, _ = exchangeWire(t.Context(), ep, dnsQuery("tc-guard.example"), true, 0)
		if f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 0 {
			t.Fatalf("invalid truncated reply opened TCP: variant=%d UDP=%d TCP=%d", mode, f.udpCalls.Load(), f.tcpCalls.Load())
		}
	}
}

func TestUDPTruncationTCPFailureCountsBothAttempts(t *testing.T) {
	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	udp, err := net.ListenPacket("udp4", address)
	if err != nil {
		reserved.Close()
		t.Fatal(err)
	}
	reserved.Close()
	defer udp.Close()
	go func() {
		var packet [4096]byte
		n, peer, err := udp.ReadFrom(packet[:])
		if err == nil {
			packet[2] |= 0x82
			udp.WriteTo(packet[:n], peer)
		}
	}()
	_, err = exchangeWire(t.Context(), Endpoint{Protocol: "udp", Address: address}, dnsQuery("tc-failed.example"), true, 0)
	var detail *upstreamExchangeError
	if !errors.As(err, &detail) || detail.protocol != "tcp" || detail.attempts != 2 {
		t.Fatalf("UDP+TCP failure lost terminal transport or attempt count: %+v, %v", detail, err)
	}
}

func TestUDPTruncationDoesNotReplayTransfers(t *testing.T) {
	for _, kind := range []uint16{251, 252} {
		f := newAutomaticDNSFixture(t)
		f.udpTruncated.Store(true)
		f.udpAnswers.Store(true)
		ep := f.endpoint
		ep.Protocol = "udp"
		q := dnsQuery("transfer.example")
		binary.BigEndian.PutUint16(q[len(q)-4:len(q)-2], kind)
		_, _ = exchangeWire(t.Context(), ep, q, true, 0)
		if f.tcpCalls.Load() != 0 {
			t.Fatalf("transfer replayed over TCP after TC: type=%d", kind)
		}
	}
}

func TestUDPValidTruncationKeepsTCPFallback(t *testing.T) {
	for _, mode := range []uint32{0, 5, 6} {
		f := newAutomaticDNSFixture(t)
		f.udpTruncated.Store(true)
		f.udpTCVariant.Store(mode)
		f.udpAnswers.Store(true)
		ep := f.endpoint
		ep.Protocol = "udp"
		q := dnsQuery("valid-tc.example")
		answer, err := exchangeWire(t.Context(), ep, q, true, 0)
		if err != nil || !matchingQuestion(q, answer) || f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 1 {
			t.Fatalf("valid TC fallback failed: mode=%d err=%v", mode, err)
		}
	}
}
