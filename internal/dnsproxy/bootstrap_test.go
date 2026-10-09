package dnsproxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestBootstrapDirectUsesIndependentNumericEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var length [2]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(length[:]))
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		query[2], query[3] = 0x81, 0x80
		_, _ = conn.Write(append(length[:], query...))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ip, err := BootstrapDirect(ctx, "tcp://"+listener.Addr().String())
	if err != nil || ip != "127.0.0.1" {
		t.Fatalf("bootstrap: ip=%q err=%v", ip, err)
	}
}
