package grpc

import (
	"context"
	"io"
	gonet "net"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/testing/servers/tcp"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	"google.golang.org/grpc/connectivity"
)

func TestGRPCRetirementKeepsLiveStreamsAndReclaimsCache(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "multi"}[multi], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			port := tcp.PickPort()
			listener, err := Listen(ctx, net.LocalHostIP, port,
				&internet.MemoryStreamConfig{ProtocolName: "grpc", ProtocolSettings: &Config{}},
				func(conn stat.Connection) { go func() { defer conn.Close(); io.Copy(conn, conn) }() })
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			dest := net.TCPDestination(net.LocalHostIP, port)
			// Listen starts its socket in a goroutine. Wait for that fixture,
			// otherwise a slow race build can fail on connection refused.
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
			for generation := 0; generation < 30; generation++ {
				settings := &internet.MemoryStreamConfig{ProtocolName: "grpc", ProtocolSettings: &Config{MultiMode: multi}}
				first, err := Dial(ctx, dest, settings)
				if err != nil {
					t.Fatal(err)
				}
				second, err := Dial(ctx, dest, settings)
				if err != nil {
					first.Close()
					t.Fatal(err)
				}
				globalDialerAccess.Lock()
				client := globalDialerMap[dialerConf{dest, settings}]
				globalDialerAccess.Unlock()
				settings.RetireDialers()
				grpcRetirementEcho(t, first)
				first.Close()
				if client.GetState() == connectivity.Shutdown {
					t.Fatal("first close killed sibling stream")
				}
				grpcRetirementEcho(t, second)
				second.Close()
				second.Close()
				assertGRPCCacheRetired(t, dest, settings)
				if client.GetState() != connectivity.Shutdown {
					t.Fatal("obsolete gRPC client remained running")
				}
				// A dispatcher which had obtained the old handler before removal
				// can still start late; its new cache must also be reclaimed.
				late, err := Dial(ctx, dest, settings)
				if err != nil {
					t.Fatal(err)
				}
				grpcRetirementEcho(t, late)
				late.Close()
				assertGRPCCacheRetired(t, dest, settings)
			}
		})
	}
}

func grpcRetirementEcho(t *testing.T, conn stat.Connection) {
	t.Helper()
	if _, err := conn.Write([]byte("live")); err != nil {
		t.Fatal(err)
	}
	var response [4]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil || string(response[:]) != "live" {
		t.Fatalf("live stream was interrupted: %q %v", response, err)
	}
}

func assertGRPCCacheRetired(t *testing.T, dest net.Destination, settings *internet.MemoryStreamConfig) {
	t.Helper()
	globalDialerAccess.Lock()
	defer globalDialerAccess.Unlock()
	if _, remains := globalDialerMap[dialerConf{dest, settings}]; remains {
		t.Fatal("retired generation remains cached")
	}
}

func TestGRPCRetirementReclaimsCanceledDial(t *testing.T) {
	settings := &internet.MemoryStreamConfig{ProtocolName: "grpc", ProtocolSettings: &Config{}}
	settings.RetireDialers()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dest := net.TCPDestination(net.LocalHostIP, 1)
	conn, err := Dial(ctx, dest, settings)
	if err == nil {
		// Stream creation is asynchronous; either the call or the returned
		// stream may observe cancellation. Both paths must release the cache.
		conn.Close()
	}
	assertGRPCCacheRetired(t, dest, settings)
}
