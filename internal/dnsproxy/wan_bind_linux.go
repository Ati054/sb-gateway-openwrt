//go:build linux

package dnsproxy

import (
	"golang.org/x/sys/unix"
	"syscall"
)

func bindWANInterface(device string) func(string, string, syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var bindErr error
		if err := raw.Control(func(fd uintptr) {
			bindErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, device)
		}); err != nil {
			return err
		}
		return bindErr
	}
}
