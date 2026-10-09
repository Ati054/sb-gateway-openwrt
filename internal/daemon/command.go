package daemon

import (
	"context"
	"os/exec"
	"time"
)

// A shell's descendants can keep output pipes open after the shell is killed.
// Bound pipe joining as well as execution, and cancel the Linux process group.
func boundedCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, name, args...)
	command.WaitDelay = time.Second
	configureCommandCancellation(command)
	return command
}
