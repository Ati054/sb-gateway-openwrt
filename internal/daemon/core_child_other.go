//go:build !linux

package daemon

import "os/exec"

func setCoreParentDeath(child *exec.Cmd) {}
