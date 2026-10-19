package xray

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	stats "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
)

type responsiveCounters struct {
	stats.UnimplementedStatsServiceServer
}

func (*responsiveCounters) QueryStats(context.Context, *stats.QueryStatsRequest) (*stats.QueryStatsResponse, error) {
	return &stats.QueryStatsResponse{}, nil
}
func (*responsiveCounters) GetStats(_ context.Context, r *stats.GetStatsRequest) (*stats.GetStatsResponse, error) {
	return &stats.GetStatsResponse{Stat: &stats.Stat{Name: r.Name}}, nil
}

// A stalled HTTP/2 mutation transport must not be mistaken for a dead core.
// Keep the backend responsive and pause only the first connection in a TCP
// relay; a separate heartbeat transport can still reach the same service.
func TestWatchdogSurvivesStalledMutationTransport(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := grpc.NewServer()
	stats.RegisterStatsServiceServer(service, &responsiveCounters{})
	go service.Serve(backend)
	defer service.Stop()
	relay, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	var paused atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var signal sync.Once
	var lock sync.Mutex
	var sockets []net.Conn
	defer func() {
		close(release)
		lock.Lock()
		defer lock.Unlock()
		for _, c := range sockets {
			c.Close()
		}
	}()
	go func() {
		for i := 0; ; i++ {
			client, err := relay.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", backend.Addr().String())
			if err != nil {
				client.Close()
				return
			}
			lock.Lock()
			sockets = append(sockets, client, server)
			lock.Unlock()
			go func() { _, _ = io.Copy(client, server) }()
			go func(first bool) {
				buffer := make([]byte, 4096)
				for {
					n, err := client.Read(buffer)
					if first && paused.Load() {
						signal.Do(func() { close(entered) })
						<-release
					}
					if n > 0 {
						if _, e := server.Write(buffer[:n]); e != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(i == 0)
		}
	}()
	client, err := NewAPIClient(relay.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = client.Command(ctx, "", "api", "statsquery"); err != nil {
		t.Fatal(err)
	}
	paused.Store(true)
	query, stopQuery := context.WithCancel(ctx)
	defer stopQuery()
	go func() { _, _ = client.Command(query, "", "api", "statsquery") }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("mutation connection did not stall")
	}
	manager := NewManager("unused", relay.Addr().String(), t.TempDir())
	manager.Run = client.Command
	probe, stopProbe := context.WithTimeout(ctx, time.Second)
	defer stopProbe()
	if err := manager.CheckAPI(probe); err != nil {
		t.Fatal("healthy core lost heartbeat behind stalled mutation transport:", err)
	}
}
