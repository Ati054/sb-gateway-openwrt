//go:build linux

package daemon

import (
	"os/exec"
	"syscall"
)

func setCoreParentDeath(child *exec.Cmd) {
	// A fatal controller exit must not leave an unowned core behind and prevent
	// procd from starting the next controller. Graceful exits close it normally.
	child.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
