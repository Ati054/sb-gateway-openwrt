//go:build !linux

package sip003

import "os/exec"

func configureChild(c *exec.Cmd) {}
