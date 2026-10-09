//go:build !linux

package daemon

import "os/exec"

func configureCommandCancellation(command *exec.Cmd) {}
