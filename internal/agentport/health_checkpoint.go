package agentport

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log"
	"os"
	"time"
)

const healthCheckpointInterval = 10 * time.Second

// Samples and timestamps may share a checkpoint; routing and failures may not.
type healthCheckpointCritical struct {
	Selected, RuntimeSelected, RuntimeError, Mode string
	CandidateSignature, Measurement, Underlay     string
	Confirmed, WANFallback                        bool
	Availability, Quality                         map[string]bool
	Failures                                      map[string]int
	FailureClass                                  map[string]string
	Nodes                                         map[string]healthNode
	Working                                       *workingSelection
}

func healthCheckpointFingerprint(state healthState) ([32]byte, error) {
	critical := make(map[string]healthCheckpointCritical, len(state))
	for id, item := range state {
		if item == nil {
			continue
		}
		failures := make(map[string]int)
		for node, count := range item.AvailabilityFailures {
			if count > 0 {
				failures[node] = count
			}
		}
		critical[id] = healthCheckpointCritical{
			Selected: item.Selected, RuntimeSelected: item.RuntimeSelected, RuntimeError: item.RuntimeError, Mode: item.Mode,
			CandidateSignature: item.CandidateSignature, Measurement: item.LatencyMeasurement, Underlay: item.UnderlayFailure,
			Confirmed: item.RuntimeConfirmed, WANFallback: item.WANFallbackActive,
			Availability: item.AvailabilityOK, Quality: item.QualityOK, Failures: failures, FailureClass: item.FailureClass,
			Nodes: item.CandidateNodes, Working: item.LastWorkingSelection,
		}
	}
	body, err := json.Marshal(critical)
	return sha256.Sum256(body), err
}

func (controller *healthController) checkpointClock() time.Time {
	if controller.checkpointNow != nil {
		return controller.checkpointNow()
	}
	return time.Now()
}

func (controller *healthController) publishRoutineState(path string, state healthState, now time.Time) error {
	fingerprint, err := healthCheckpointFingerprint(state)
	if err != nil {
		return err
	}
	clock := controller.checkpointClock()
	_, statErr := os.Stat(path)
	if controller.checkpointAt.IsZero() || clock.Sub(controller.checkpointAt) >= healthCheckpointInterval ||
		fingerprint != controller.checkpointFingerprint || os.IsNotExist(statErr) {
		return controller.publishState(path, state, now)
	}
	controller.checkpointPending = true
	// The browser and DNS lease see current state even while history is batched.
	return controller.publishMonitor(path, state, now)
}

// Maintenance must not advance or postpone the original probe deadline.
func (controller *healthController) waitWithHealthCheckpoint(ctx context.Context, delay time.Duration, paths []string, poll time.Duration, signals <-chan xrayFailureSignal, accept func(xrayFailureSignal) bool, progress ...func()) bool {
	if !controller.checkpointPending {
		return waitForHealthWake(ctx, delay, paths, poll, signals, accept, progress...)
	}
	deadline, generation := time.Now().Add(delay), generationStamp(paths)
	for {
		flushAt := controller.checkpointAt.Add(healthCheckpointInterval)
		maintenance := controller.checkpointPending && flushAt.Before(deadline)
		remaining := max(time.Until(deadline), time.Duration(0))
		if maintenance {
			remaining = min(remaining, max(time.Until(flushAt), time.Duration(0)))
		}
		accepted := false
		if !waitForHealthWake(ctx, remaining, paths, poll, signals, func(signal xrayFailureSignal) bool {
			if accept != nil && accept(signal) {
				accepted = true
				return true
			}
			return false
		}, progress...) {
			return false
		}
		if accepted || generation != generationStamp(paths) || !maintenance || time.Now().Before(flushAt) {
			return true
		}
		if err := controller.publishRoutineState(statePath(controller.opts.StateRoot, "selector-health"), controller.state, time.Now()); err != nil {
			log.Printf("agent: health checkpoint flush failed: %v", err)
			return waitForHealthWake(ctx, max(time.Until(deadline), time.Duration(0)), paths, poll, signals, accept, progress...)
		}
	}
}
