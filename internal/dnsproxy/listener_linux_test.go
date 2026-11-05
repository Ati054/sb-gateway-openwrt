//go:build linux

package dnsproxy

import (
	"context"
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDNSListenerUsesTCPDespiteMultipathRuntimeDefault(t *testing.T) {
	t.Setenv("GODEBUG", "multipathtcp=3")
	config := dnsListenConfig()
	listener, err := config.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	raw, err := listener.(*net.TCPListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	protocol := 0
	var socketErr error
	err = raw.Control(func(fd uintptr) { protocol, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PROTOCOL) })
	if err != nil || socketErr != nil || protocol != unix.IPPROTO_TCP {
		t.Fatalf("DNS socket protocol %d: %v %v", protocol, err, socketErr)
	}
}
