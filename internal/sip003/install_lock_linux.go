//go:build linux

package sip003

import (
	"os"
	"syscall"
)

func lockPluginInstallFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
