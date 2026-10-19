//go:build linux

package dnsproxy

import (
	"net"

	"golang.org/x/sys/unix"
)

func setUDPReadBuffer(conn *net.UDPConn, requested int) (int, error) {
	if err := conn.SetReadBuffer(requested); err != nil {
		return 0, err
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	actual := 0
	err = raw.Control(func(fd uintptr) {
		actual, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
		if actual >= 2*requested {
			return
		}
		// OpenWrt's rmem_max can silently clamp SO_RCVBUF below our budget.
		// Reserve only this socket's bounded queue (at most 2 MiB including
		// Linux's accounting overhead); never change a system-wide limit.
		// Without the required privilege, keep the ordinary socket buffer.
		if unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, requested) == nil {
			actual, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
		}
	})
	return actual, err
}
