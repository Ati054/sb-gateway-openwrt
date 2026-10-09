package agentport

import (
	"context"
	"errors"
	"net"
	"time"
)

var errProbeSchedulingDelayed = errors.New("local probe deadline handling was delayed")

// A remote timeout normally completes around its own deadline. If even deadline
// handling runs substantially late, the device failed to schedule the check on
// time. Defer that sample; do not turn local scheduling into node failure or a
// quality win. Prompt remote timeout, TLS and fatal network errors retain their
// existing semantics. Cancellation still interrupts and joins probe workers.
func probeDeadlineResult(parent context.Context, started, finished time.Time, budget time.Duration, err error) error {
	if parent.Err() != nil || budget <= 0 {
		return err
	}
	grace := max(500*time.Millisecond, budget/4)
	if finished.Sub(started) <= budget+grace {
		return err
	}
	var timeout net.Error
	if err == nil || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return errors.Join(errProbeSchedulingDelayed, err)
	}
	return err
}
func localProbeError(evidence probeEvidence) error {
	if evidence.LocalError != nil {
		return evidence.LocalError
	}
	return errProbeSelectorUnavailable
}
