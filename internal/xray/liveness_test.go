package xray

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func livenessSOCKS(t *testing.T, reply []byte) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request [3]byte
		if _, err = io.ReadFull(conn, request[:]); err != nil {
			return
		}
		if request != [3]byte{5, 1, 0} {
			return
		}
		if reply != nil {
			_, _ = conn.Write(reply)
			return
		}
		_, _ = io.Copy(io.Discard, conn)
	}()
	t.Cleanup(func() {
		listener.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("liveness connection leaked")
		}
	})
	return listener.Addr().String()
}

func TestLivenessKeepsRespondingProxyWhenAPIStalls(t *testing.T) {
	manager := NewManager("unused", "unused", t.TempDir())
	manager.LivenessSOCKS = livenessSOCKS(t, []byte{5, 0})
	canceled := make(chan struct{})
	manager.Run = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		defer close(canceled)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.CheckLiveness(ctx); err != nil {
		t.Fatal("responding proxy was declared hung:", err)
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("late API request was not canceled")
	}
}

func TestLivenessDoesNotAcceptOpenOrWrongProtocolSocket(t *testing.T) {
	for name, reply := range map[string][]byte{"silent": nil, "auth-required": {5, 2}, "truncated": {5}} {
		t.Run(name, func(t *testing.T) {
			manager := NewManager("unused", "unused", t.TempDir())
			manager.LivenessSOCKS = livenessSOCKS(t, reply)
			manager.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("API unavailable") }
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if err := manager.CheckLiveness(ctx); err == nil {
				t.Fatal("unresponsive or invalid proxy accepted")
			}
		})
	}
}

func TestLivenessHealthyAPIDoesNotRequireProxyFallback(t *testing.T) {
	manager := NewManager("unused", "unused", t.TempDir())
	manager.LivenessSOCKS = "invalid address"
	manager.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	if err := manager.CheckLiveness(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLivenessCancellationDoesNotStartWork(t *testing.T) {
	manager := NewManager("unused", "unused", t.TempDir())
	manager.LivenessSOCKS = "invalid address"
	manager.Run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("canceled check started API work")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.CheckLiveness(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
