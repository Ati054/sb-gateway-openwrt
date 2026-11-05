//go:build linux

package xray

import (
	"context"
	"net"
	"testing"

	"github.com/xtls/xray-core/transport/internet"
	"golang.org/x/sys/unix"
)

func TestXrayListenerMPTCPDisabledByDefault(t *testing.T) {
	t.Setenv("GODEBUG", "multipathtcp=3")
	for _, options := range []*internet.SocketConfig{nil, {}} {
		listener, err := new(internet.DefaultListener).Listen(context.Background(), &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, options)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer listener.Close()
			tcp, ok := listener.(*net.TCPListener)
			if !ok {
				t.Fatal("unexpected TCP listener")
			}
			raw, err := tcp.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			protocol := 0
			var socketErr error
			err = raw.Control(func(fd uintptr) { protocol, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PROTOCOL) })
			if err != nil || socketErr != nil || protocol != unix.IPPROTO_TCP {
				t.Fatalf("listener protocol %d: %v %v", protocol, err, socketErr)
			}
		}()
	}
}
