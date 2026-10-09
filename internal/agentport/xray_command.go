// Adapted from SB Gateway 1.6.45 (AGPL-3.0).
package agentport

import (
	"context"
	"os/exec"
	"time"
)

// Bound API handler preparation separately from HTTPS workers, including the
// embedded client. Release the slot before the network probe begins.
// The independent live slot must never queue behind background preparations.
var xrayCommandSlot = make(chan struct{}, 1)
var xrayProbeCommandSlots = make(chan struct{}, 2)

type backgroundXrayCommandKey struct{}

func backgroundXrayCommandContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundXrayCommandKey{}, true)
}

func runXrayCommand(parent context.Context, timeout time.Duration, binary string, arguments ...string) (output []byte, err error) {
	return runXrayCommandWithRunner(parent, timeout, nil, binary, arguments...)
}

func runXrayCommandWithRunner(parent context.Context, timeout time.Duration, runner func(context.Context, string, ...string) ([]byte, error), binary string, arguments ...string) (output []byte, err error) {
	slot := xrayCommandSlot
	lane := "active"
	if parent.Value(backgroundXrayCommandKey{}) == true {
		slot = xrayProbeCommandSlots
		lane = "background"
	}
	trace := beginHealthStage("api", lane, "", "")
	if len(arguments) > 1 && arguments[0] == "api" {
		trace.command(arguments[1])
	}
	defer func() { trace.finish(err == nil) }()
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
		trace.acquiredSlot()
	case <-parent.Done():
		return nil, parent.Err()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if runner != nil {
		return runner(ctx, binary, arguments...)
	}
	command := exec.CommandContext(ctx, binary, arguments...)
	output, err = command.CombinedOutput()
	if command.Process != nil && ctx.Err() != nil {
		return output, ctx.Err()
	}
	return output, err
}
