package dnsproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestWireDeadlineRetainsTimeoutAfterSocketClose(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(io.Discard, c)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	endpoint := Endpoint{Protocol: "tcp", Address: listener.Addr().String()}
	_, err = exchangeWire(ctx, endpoint, dnsQuery("timeout.example.test"), true, 0)
	var networkErr net.Error
	if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &networkErr) && networkErr.Timeout()) {
		t.Fatalf("deadline became an ordinary transport error: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed-out DNS socket was not closed")
	}
}
