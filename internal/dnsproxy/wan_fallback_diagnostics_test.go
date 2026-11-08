package dnsproxy

import (
	"net"
	"testing"
)

func TestWANTCPFallbackFailureReportsActualProtocol(t *testing.T) {
	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	socket, err := net.ListenPacket("udp4", address)
	if err != nil {
		reserved.Close()
		t.Fatal(err)
	}
	reserved.Close()
	defer socket.Close()
	go func() {
		var buffer [4096]byte
		n, a, e := socket.ReadFrom(buffer[:])
		if e == nil {
			buffer[2] |= 0x82
			_, _ = socket.WriteTo(buffer[:n], a)
		}
	}()
	_, err = exchangeWANDNS(t.Context(), &wanDNSSnapshot{servers: []Endpoint{{Protocol: "udp", Address: address}}}, dnsQuery("fallback.example.test"))
	if err == nil {
		t.Fatal("unavailable TCP server succeeded")
	}
	diagnostics := &upstreamFailureDiagnostics{}
	diagnostics.record(true, "udp", err)
	if got := diagnostics.paths[1].last.Protocol; got != "tcp" {
		t.Fatalf("fallback mislabeled as %q", got)
	}
}
