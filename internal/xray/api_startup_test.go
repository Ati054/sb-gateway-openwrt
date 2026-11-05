package xray

import (
	"context"
	"net"
	"testing"
	"time"

	router "github.com/xtls/xray-core/app/router/command"
	"google.golang.org/grpc"
)

type startupRouteServer struct {
	router.UnimplementedRoutingServiceServer
}

func (startupRouteServer) GetBalancerInfo(context.Context, *router.GetBalancerInfoRequest) (*router.GetBalancerInfoResponse, error) {
	return &router.GetBalancerInfoResponse{Balancer: &router.BalancerMsg{Override: &router.OverrideInfo{Target: "block"}}}, nil
}

func TestLocalAPIRecoversPromptlyWhenCoreStartsAfterClient(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	reserved.Close()
	client, err := NewAPIClient(address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	first, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, err = client.Command(first, "unused", "api", "bi", "route")
	cancel()
	if err == nil {
		t.Fatal("fixture contacted a core before it was started")
	}
	time.Sleep(150 * time.Millisecond)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	router.RegisterRoutingServiceServer(server, startupRouteServer{})
	defer server.Stop()
	go server.Serve(listener)
	ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
	defer cancel()
	for {
		body, err := client.Command(ctx, "unused", "api", "bi", "route")
		if err == nil {
			if len(body) == 0 {
				t.Fatal("empty core readback")
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("a ready localhost core remained hidden by reconnect backoff: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
