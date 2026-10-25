//go:build linux

package daemon

import (
	"os"
	"syscall"
)

func lockCatalogFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
