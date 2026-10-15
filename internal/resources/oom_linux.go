//go:build linux

package resources

import (
	"syscall"
)

func KernelKilledForOOM(pid int, since float64) bool {
	if pid <= 0 || since <= 0 {
		return false
	}
	fd, err := syscall.Open("/dev/kmsg", syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	buf := make([]byte, 16<<10)
	for n := 0; n < 2048; n++ {
		read, err := syscall.Read(fd, buf)
		if err == syscall.EPIPE {
			continue
		}
		if err != nil || read == 0 {
			return false
		}
		if oomRecord(string(buf[:read]), pid, since) {
			return true
		}
	}
	return false
}
