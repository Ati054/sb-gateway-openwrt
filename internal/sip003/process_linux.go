package sip003

import (
	"os/exec"
	"syscall"
)

func configureChild(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM} }
