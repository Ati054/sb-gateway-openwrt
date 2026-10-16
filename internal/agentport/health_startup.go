package agentport

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// InvalidateRuntimeHealth preserves measurements and the remembered working
// route, but never carries an old core's readback into a new DNS/routing runtime.
func InvalidateRuntimeHealth(root string) error {
	path := statePath(root, "selector-health")
	state := healthState{}
	if err := readJSON(path, &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return (&healthController{}).publishMonitor(path, state, time.Now())
		}
		return err
	}
	if state == nil {
		return errors.New("selector health state is null")
	}
	for _, item := range state {
		if item == nil {
			continue
		}
		retainWorkingSelection(item)
		item.RuntimeConfirmed = false
		item.RuntimeSelected, item.RuntimeError, item.RuntimeObservedAt = "", "", ""
		item.WANFallbackActive = false
	}
	if err := writeJSONAtomic(path, state); err != nil {
		return err
	}
	return (&healthController{}).publishMonitor(path, state, time.Now())
}

func retainWorkingSelection(item *policyHealthState) {
	if saved := savedWorkingSelection(item); saved != nil {
		value := *saved
		item.LastWorkingSelection = &value
	}
}

// Restore remembered paths for every sheet before any routine quality survey.
// Otherwise a slow survey in one sheet leaves unrelated clients on block.
func (controller *healthController) restoreWarmSelections(now time.Time, pool healthPool, ids []string) error {
	warm := []string{}
	for _, id := range ids {
		if controller.warmStarted[id] {
			continue
		}
		item := controller.state[id]
		if item == nil {
			continue
		}
		ensureHealthMaps(item)
		actual, err := controller.runtime.Current(id)
		// Hot membership edits retain a live old handler until its replacement
		// is proven. Only a blocked core needs startup restoration here.
		if err != nil || actual != "block" || (item.Selected == "block" && !restorableWorkingSelection(pool.HealthPolicies[id], item)) {
			continue
		}
		retainWorkingSelection(item)
		item.RuntimeConfirmed, item.WANFallbackActive = false, false
		item.RuntimeSelected = ""
		warm = append(warm, id)
	}
	if len(warm) == 0 {
		return nil
	}
	path := statePath(controller.opts.StateRoot, "selector-health")
	if err := controller.publishState(path, controller.state, now); err != nil {
		return err
	}
	var failures []error
	for _, id := range warm {
		item, contract := controller.state[id], pool.HealthPolicies[id]
		actual, err := controller.runtime.Current(id)
		if err == nil && actual == "block" && restorableWorkingSelection(contract, item) {
			saved := savedWorkingSelection(item).Selected
			evidence := controller.runtime.ProbeAvailability(saved)
			if interruption := takeProbeInterruption(controller.runtime); interruption != nil {
				return interruption
			}
			if !evidence.OK && !evidence.LocalFailure && evidence.Failure == probeFailureTimeout {
				if confirmation, ok := controller.runtime.(activeConfirmationRuntime); ok {
					evidence = confirmation.ProbeAvailabilityConfirmation(saved)
					if interruption := takeProbeInterruption(controller.runtime); interruption != nil {
						return interruption
					}
				}
			}
			if evidence.LocalFailure {
				err = errProbeSelectorUnavailable
			} else if evidence.OK {
				err = controller.runtime.Select(id, saved)
				if err == nil {
					actual, err = controller.runtime.Current(id)
				}
				if err == nil && actual != saved {
					err = errors.New("restored selector readback differs")
				}
			}
		}
		if err != nil {
			item.RuntimeError = err.Error()
			failures = append(failures, fmt.Errorf("restore policy %s: %w", id, err))
		} else if actual == "block" || contains(contract.Candidates, actual) {
			item.RuntimeSelected, item.RuntimeConfirmed = actual, true
			item.RuntimeError = ""
		}
		item.RuntimeObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := controller.publishState(path, controller.state, now); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}
