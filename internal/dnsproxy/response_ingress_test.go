package dnsproxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func responseIngressPackets() (response, request []byte) {
	response = dnsQuery("response.example.test")
	response[2] |= 0x80
	request = dnsQuery("request.example.test")
	request[0], request[1], request[5] = 0x56, 0x78, 2
	return
}

func TestDNSResponsePacketIgnoredBeforeUpstream(t *testing.T) {
	s := rejectedHeaderFixture(t)
	response, _ := responseIngressPackets()
	if body := s.resolve(t.Context(), response, netip.MustParseAddr("192.0.2.8")); len(body) != 0 {
		t.Fatal("a response sent to the query listener received another response")
	}
	if s.invalidQueryReasons[queryResponsePacket].Load() != 1 || s.failureInvalidQuery.Load() != 1 || s.failures.Load() != 1 || s.upstreamExchanges.Load() != 0 || s.activeQueries.Load() != 0 {
		t.Fatal("ignored response was hidden from accounting or reached upstream")
	}
}

func TestDNSResponsePacketUDPSendsNoDatagram(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	s := rejectedHeaderFixture(t)
	go func() { defer close(done); s.serveUDP(ctx, listener) }()
	t.Cleanup(func() { cancel(); listener.Close(); <-done })
	client, err := net.Dial("udp4", listener.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	response, request := responseIngressPackets()
	for _, body := range [][]byte{response, request} {
		if _, err := client.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	var buffer [512]byte
	n, err := client.Read(buffer[:])
	if err != nil {
		t.Fatal(err)
	}
	// The following malformed request must still receive FORMERR. A reply to
	// the preceding response, including a zero-length datagram, fails here.
	if n != 12 || binary.BigEndian.Uint16(buffer[:2]) != 0x5678 || buffer[3]&15 != 1 {
		t.Fatal("UDP replied to an incoming response or lost the next request")
	}
	entries := readRejectedHeaders(t, s)
	if len(entries) != 2 {
		t.Fatal("UDP did not retain both rejection reasons")
	}
	for _, entry := range entries {
		if entry.Transport != "udp" || entry.Source != "127.0.0.1" || entry.SourcePort != addrPort(client.LocalAddr()).Port() {
			t.Fatal("UDP diagnostics lost the real ingress or source")
		}
	}
}

func TestDNSResponsePacketTCPSendsNoFrameAndKeepsReading(t *testing.T) {
	serverConn, client := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	s := rejectedHeaderFixture(t)
	go func() {
		defer close(done)
		defer serverConn.Close()
		s.handleTCP(ctx, serverConn)
	}()
	t.Cleanup(func() { cancel(); client.Close(); serverConn.Close(); <-done })
	client.SetDeadline(time.Now().Add(5 * time.Second))
	response, request := responseIngressPackets()
	var frames []byte
	for _, body := range [][]byte{response, request} {
		frames = append(frames, byte(len(body)>>8), byte(len(body)))
		frames = append(frames, body...)
	}
	written := make(chan error, 1)
	go func() { _, err := client.Write(frames); written <- err }()
	var prefix [2]byte
	if _, err := io.ReadFull(client, prefix[:]); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(prefix[:]) != 12 {
		t.Fatal("TCP emitted an empty or unexpected response frame")
	}
	var body [12]byte
	if _, err := io.ReadFull(client, body[:]); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(body[:2]) != 0x5678 || body[3]&15 != 1 {
		t.Fatal("TCP replied to an incoming response or lost the following request")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	entries := readRejectedHeaders(t, s)
	if len(entries) != 2 {
		t.Fatal("TCP did not retain both rejection reasons")
	}
	for _, entry := range entries {
		if entry.Transport != "tcp" || entry.Source != "unknown" {
			t.Fatal("TCP diagnostics lost the ingress or mishandled a non-IP test connection")
		}
	}
}
