package dnsproxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseEndpoint(t *testing.T) {
	for _, item := range []struct {
		address string
		direct  bool
		want    string
	}{
		{"https://dns.google/dns-query", false, "doh"},
		{"tls://dns.google:853", false, "dot"},
		{"tcp://1.1.1.1:53", false, "tcp"},
		{"udp://1.1.1.1:53", true, "udp"},
	} {
		parsed, err := ParseEndpoint(item.address, item.direct)
		if err != nil || parsed.Protocol != item.want {
			t.Fatalf("endpoint %q: %+v %v", item.address, parsed, err)
		}
	}
	for _, item := range []struct {
		address string
		direct  bool
	}{
		{"udp://1.1.1.1:53", false},
		{"tcp://dns.google:53", false},
		{"tls://user:pass@dns.google:853", false},
		{"tls://dns.google:853/path", false},
		{"https://dns.google", false},
		{"tcp://1.1.1.1:70000", true},
	} {
		if _, err := ParseEndpoint(item.address, item.direct); err == nil {
			t.Fatalf("unsafe endpoint accepted: %q", item.address)
		}
	}
}

func respondDNS(conn net.Conn) error {
	var size [2]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return err
	}
	query := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(conn, query); err != nil {
		return err
	}
	query[2], query[3] = 0x81, 0x80
	if _, err := conn.Write(size[:]); err != nil {
		return err
	}
	_, err := conn.Write(query)
	return err
}

func mockCONNECT(t *testing.T) (int, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	count := new(atomic.Int32)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				line, err := reader.ReadString('\n')
				if err != nil || !strings.HasPrefix(line, "CONNECT 1.1.1.1:53 HTTP/1.1") {
					return
				}
				for {
					line, err = reader.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
				}
				count.Add(1)
				if _, err := fmt.Fprint(conn, "HTTP/1.1 200 Connection established\r\n\r\n"); err == nil {
					_ = respondDNS(conn)
				}
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, count
}

func TestPlainDNSOverTCPUsesSelectedPolicyProxy(t *testing.T) {
	mainPort, mainCalls := mockCONNECT(t)
	otherPort, otherCalls := mockCONNECT(t)
	s := &server{
		config: Config{Mode: "proxy-only", DefaultPolicy: "main", ProxyPort: mainPort,
			ProxyURL: "tcp://1.1.1.1:53", PolicyProxyPorts: map[string]int{
				"main": mainPort, "other": otherPort,
			}},
		prefixes: []policyPrefix{{id: "other", prefix: netip.MustParsePrefix("192.0.2.0/24")}},
		cache:    make(map[string]*cacheEntry),
	}
	query := dnsQuery("example.com")
	for _, address := range []string{"198.51.100.3", "192.0.2.3"} {
		response := s.resolve(context.Background(), query, netip.MustParseAddr(address))
		if len(response) != len(query) || response[3] != 0x80 {
			t.Fatalf("DNS over selected policy failed: %x", response)
		}
	}
	if mainCalls.Load() != 1 || otherCalls.Load() != 1 {
		t.Fatalf("DNS used wrong route: main=%d other=%d", mainCalls.Load(), otherCalls.Load())
	}
}

func TestPlainDNSOverUDPDirect(t *testing.T) {
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		buffer := make([]byte, maxDNSMessage)
		n, address, err := listener.ReadFrom(buffer)
		if err == nil && n >= 12 {
			buffer[2], buffer[3] = 0x81, 0x80
			_, _ = listener.WriteTo(buffer[:n], address)
		}
	}()
	query := dnsQuery("example.com")
	response, err := exchangeWire(context.Background(), Endpoint{
		Protocol: "udp", Address: listener.LocalAddr().String(),
	}, query, true, 0)
	if err != nil || len(response) != len(query) || response[3] != 0x80 {
		t.Fatalf("direct UDP DNS failed: %x %v", response, err)
	}
}

func TestDoTDoesNotAcceptUntrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "https://")
	_, err := exchangeWire(context.Background(), Endpoint{
		Protocol: "dot", Address: address, ServerName: "localhost",
	}, dnsQuery("example.com"), true, 0)
	if err == nil {
		t.Fatal("untrusted DoT certificate was accepted")
	}
}
