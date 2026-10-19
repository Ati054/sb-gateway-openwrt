package xray

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	stats "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
)

func TestWatchdogAPIIsIndependentOfSelectorMutation(t *testing.T) {
	m := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	called := make(chan struct{}, 1)
	m.Run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if binary != "xray" || len(args) != 3 || args[0] != "api" || args[1] != "statssys" {
			t.Error("unexpected watchdog operation")
		}
		called <- struct{}{}
		return nil, ctx.Err()
	}
	m.mu.Lock() // A long subscription/selector transaction is still in progress.
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.CheckAPI(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("watchdog waited behind selector mutation")
	}
	<-called
	cancel()
	if err := m.CheckAPI(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled watchdog check ignored its deadline")
	}
}

type busyCountersServer struct {
	stats.UnimplementedStatsServiceServer
	started chan struct{}
	once    sync.Once
}

func (s *busyCountersServer) QueryStats(ctx context.Context, _ *stats.QueryStatsRequest) (*stats.QueryStatsResponse, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*busyCountersServer) GetSysStats(context.Context, *stats.SysStatsRequest) (*stats.SysStatsResponse, error) {
	return &stats.SysStatsResponse{NumGoroutine: 1, Alloc: 1024}, nil
}

func TestWatchdogDoesNotWaitForBusyTrafficCounters(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := grpc.NewServer()
	busy := &busyCountersServer{started: make(chan struct{})}
	stats.RegisterStatsServiceServer(service, busy)
	go service.Serve(listener)
	defer service.Stop()
	client, err := NewAPIClient(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = client.Command(ctx, "", "api", "statsquery") }()
	select {
	case <-busy.started:
	case <-time.After(3 * time.Second):
		t.Fatal("counter request did not reach the core")
	}
	m := NewManager("unused", listener.Addr().String(), t.TempDir())
	m.Run = client.Command
	probe, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if err := m.CheckAPI(probe); err != nil {
		t.Fatal("healthy runtime was confused with busy counters:", err)
	}
	cancel()
	<-done
}
