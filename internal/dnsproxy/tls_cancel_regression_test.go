package dnsproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestDoHCancelledTLSHandshakeReleasesConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := listener.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	tr := &http.Transport{ForceAttemptHTTP2: true}
	client := newDoHClient(tr)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = exchangeDoH(ctx, client, "https://"+listener.Addr().String()+"/dns-query", dnsQuery("example.org"))
	if err == nil {
		t.Fatal("stalled TLS handshake succeeded")
	}
	var c net.Conn
	select {
	case c = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("no TLS connection")
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	_, err = io.Copy(io.Discard, c)
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("cancelled DNS lookup retained an unfinished TLS connection")
	}
}
