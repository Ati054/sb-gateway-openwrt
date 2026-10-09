package outbound

import (
	"context"
	"io"
	gonet "net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol/tls/cert"
	"github.com/xtls/xray-core/proxy/hysteria/account"
	"github.com/xtls/xray-core/testing/servers/tcp"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/grpc"
	"github.com/xtls/xray-core/transport/internet/hysteria"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/internet/tls"
)

func TestRetirementPreservesStreamsAcrossAToBToA(t *testing.T) {
	for _, transport := range []string{"grpc", "grpc-multi", "packet-up", "stream-up", "stream-one", "hysteria", "hysteria-udp"} {
		for _, replace := range []bool{false, true} {
			name := transport + "/same-handler"
			if replace {
				name = transport + "/new-handler-same-tag"
			}
			t.Run(name, func(t *testing.T) {
				dest, settings := retirementTransport(t, transport)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if transport == "hysteria-udp" {
					ctx = hysteria.ContextWithDatagram(ctx, true)
				}
				manager, err := New(ctx, &proxyman.OutboundConfig{})
				if err != nil {
					t.Fatal(err)
				}
				var released []*atomic.Bool
				add := func(tag string) *Handler {
					handler := &Handler{tag: tag, streamSettings: settings()}
					closed := &atomic.Bool{}
					release := handler.streamSettings.AcquireDialer()
					handler.streamSettings.RegisterDialerCleanup("assert-drained", func() { closed.Store(true) })
					release()
					released = append(released, closed)
					if err := manager.AddHandler(ctx, handler); err != nil {
						t.Fatal(err)
					}
					return handler
				}
				dial := func(tag string) stat.Connection {
					conn, err := manager.GetHandler(tag).(*Handler).Dial(ctx, dest)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { conn.Close() })
					return conn
				}
				remove := func(tag string) {
					if err := manager.RemoveHandler(ctx, tag); err != nil {
						t.Fatal(err)
					}
				}
				echo := func(conns ...stat.Connection) {
					for _, conn := range conns {
						retirementHandlerEcho(t, conn, transport == "hysteria-udp")
					}
				}
				a := add("a")
				add("b")
				firstA := dial("a")
				echo(firstA)
				// Switch new connections to B, then remove A while its original
				// connection is still open. HandlerService must not close it.
				liveB := dial("b")
				remove("a")
				echo(firstA, liveB)
				if replace {
					add("a")
				} else if err := manager.AddHandler(ctx, a); err != nil {
					t.Fatal(err)
				}
				secondA := dial("a")
				remove("b")
				echo(firstA, liveB, secondA)
				remove("a")
				for _, closed := range released {
					if closed.Load() {
						t.Fatal("cleanup ran with an original connection still alive")
					}
				}
				echo(firstA, liveB, secondA)
				secondA.Close()
				echo(firstA, liveB)
				liveB.Close()
				echo(firstA)
				firstA.Close()
				deadline := time.Now().Add(3 * time.Second)
				for _, closed := range released {
					for !closed.Load() && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					if !closed.Load() {
						t.Fatal("drained handler retained its transport resources")
					}
				}
			})
		}
	}
}

func retirementTransport(t *testing.T, transport string) (net.Destination, func() *internet.MemoryStreamConfig) {
	t.Helper()
	ctx := context.Background()
	echo := func(conn stat.Connection) { go func() { defer conn.Close(); io.Copy(conn, conn) }() }
	port := tcp.PickPort()
	dest := net.TCPDestination(net.LocalHostIP, port)
	var listener internet.Listener
	var err error
	var settings func() *internet.MemoryStreamConfig
	switch {
	case strings.HasPrefix(transport, "grpc"):
		settings = func() *internet.MemoryStreamConfig {
			return &internet.MemoryStreamConfig{ProtocolName: "grpc", ProtocolSettings: &grpc.Config{MultiMode: transport == "grpc-multi"}}
		}
		listener, err = grpc.Listen(ctx, net.LocalHostIP, port, settings(), echo)
	default:
		certificate, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
		serverCertificate := tls.ParseCertificate(certificate)
		serverCertificate.OneTimeLoading = true
		serverTLS := &tls.Config{Certificate: []*tls.Certificate{serverCertificate}}
		if strings.HasPrefix(transport, "hysteria") {
			settings = func() *internet.MemoryStreamConfig {
				return &internet.MemoryStreamConfig{ProtocolName: "hysteria", ProtocolSettings: &hysteria.Config{Auth: "retirement-test"},
					SecurityType: "tls", SecuritySettings: &tls.Config{PinnedPeerCertSha256: [][]byte{hash[:]}},
					QuicParams: &internet.QuicParams{DisableChromeParrot: true}}
			}
			listener, err = hysteria.Listen(hysteria.ContextWithValidator(ctx, account.NewValidator()), net.LocalHostIP, 0,
				&internet.MemoryStreamConfig{ProtocolName: "hysteria", ProtocolSettings: &hysteria.Config{Auth: "retirement-test", UdpIdleTimeout: 60},
					SecurityType: "tls", SecuritySettings: serverTLS}, echo)
			if err == nil {
				dest.Port = net.Port(listener.Addr().(*net.UDPAddr).Port)
			}
		} else {
			settings = func() *internet.MemoryStreamConfig {
				return &internet.MemoryStreamConfig{ProtocolName: "splithttp", ProtocolSettings: &splithttp.Config{Mode: transport},
					SecurityType: "tls", SecuritySettings: &tls.Config{PinnedPeerCertSha256: [][]byte{hash[:]}}}
			}
			listener, err = splithttp.ListenXH(ctx, net.LocalHostIP, port,
				&internet.MemoryStreamConfig{ProtocolName: "splithttp", ProtocolSettings: &splithttp.Config{},
					SecurityType: "tls", SecuritySettings: serverTLS}, echo)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	if strings.HasPrefix(transport, "grpc") {
		// The upstream gRPC listener binds asynchronously.
		deadline := time.Now().Add(3 * time.Second)
		for {
			ready, err := gonet.DialTimeout("tcp", dest.NetAddr(), 100*time.Millisecond)
			if err == nil {
				ready.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("gRPC test listener did not start: ", err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	return dest, settings
}

func retirementHandlerEcho(t *testing.T, conn stat.Connection, datagram bool) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		payload := []byte("live")
		if datagram {
			payload = append(make([]byte, 4), payload...)
		}
		_, err := conn.Write(payload)
		if err == nil {
			response := make([]byte, len(payload))
			_, err = io.ReadFull(conn, response)
			if err == nil && string(response[len(response)-4:]) != "live" {
				err = io.ErrUnexpectedEOF
			}
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("existing connection interrupted: %v", err)
		}
	case <-time.After(5 * time.Second):
		conn.Close()
		t.Fatal("existing connection stopped across A -> B -> A")
	}
}
