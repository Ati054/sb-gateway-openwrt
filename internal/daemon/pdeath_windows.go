//go:build windows

package daemon

import "os/exec"

func configureChild(_ *exec.Cmd) {}
