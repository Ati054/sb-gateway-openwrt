//go:build linux

package daemon

import (
	"os/exec"
	"syscall"
)

func configureChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
