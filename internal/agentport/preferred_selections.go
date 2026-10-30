package agentport

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"strings"
	"time"
)

// Only the last working choice belongs on flash. Samples, failures and live
// core readback remain in RAM and cannot become evidence after a cold boot.
type preferredSelection struct {
	workingSelection
	Fingerprint string `json:"fingerprint"`
}

const preferredSelectionsMaxBytes = 1 << 20

func validPreferredSelection(value preferredSelection) bool {
	fingerprint, err := hex.DecodeString(value.Fingerprint)
	return err == nil && len(fingerprint) == 32 && value.Selected != "" &&
		value.Selected != "block" && len(value.Selected) <= 256 &&
		(value.Mode == "best" || value.Mode == "priority") &&
		contains(strings.Split(value.CandidateSignature, "\n"), value.Selected)
}

func readPreferredSelections(path string) (map[string]preferredSelection, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > preferredSelectionsMaxBytes {
		return nil, errors.New("invalid preferred selections file or size")
	}
	var saved map[string]preferredSelection
	if err := readJSON(path, &saved); err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, errors.New("preferred selections are null")
	}
	for _, value := range saved {
		if !validPreferredSelection(value) {
			return nil, errors.New("invalid preferred selection")
		}
	}
	return saved, nil
}

// RestoreColdSelections seeds only an absent RAM state. The normal warm-start
// path must still probe each saved server before admitting LAN traffic.
func RestoreColdSelections(root, preferredFile, poolFile string) error {
	path := statePath(root, "selector-health")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	saved, err := readPreferredSelections(preferredFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pool healthPool
	if err := readJSON(poolFile, &pool); err != nil {
		return err
	}
	state := make(healthState)
	for id, value := range saved {
		contract, exists := pool.HealthPolicies[id]
		if !exists || contract.Nodes[value.Selected].Fingerprint != value.Fingerprint {
			continue
		}
		choice := value.workingSelection
		item := newPolicyHealthState()
		item.Selected, item.Mode, item.CandidateSignature = choice.Selected, choice.Mode, choice.CandidateSignature
		item.LastWorkingSelection = &choice
		item.CandidateNodes = map[string]healthNode{choice.Selected: {Fingerprint: value.Fingerprint}}
		if restorableWorkingSelection(contract, item) {
			state[id] = item
		}
	}
	if len(state) == 0 {
		return nil
	}
	return writeJSONAtomic(path, state)
}

func (controller *healthController) persistPreferredSelections(state healthState, now time.Time) {
	path := controller.opts.PreferredSelectionsFile
	if path == "" || now.Before(controller.preferredRetryAt) {
		return
	}
	if !controller.preferredLoaded {
		controller.preferredSelections, _ = readPreferredSelections(path)
		controller.preferredLoaded = true
	}
	saved := make(map[string]preferredSelection)
	for id, item := range state {
		if item == nil || item.LastWorkingSelection == nil {
			continue
		}
		choice := *item.LastWorkingSelection
		value := preferredSelection{choice, item.CandidateNodes[choice.Selected].Fingerprint}
		if validPreferredSelection(value) {
			saved[id] = value
		}
	}
	if maps.Equal(saved, controller.preferredSelections) {
		return
	}
	body, err := json.Marshal(saved)
	if err == nil && len(body) > preferredSelectionsMaxBytes {
		err = fmt.Errorf("preferred selections exceed %d bytes", preferredSelectionsMaxBytes)
	}
	if err == nil {
		err = writeJSONAtomic(path, saved)
	}
	if err != nil {
		// A full/read-only flash must not interrupt a working route or cause an
		// I/O retry on every probe. The next attempt saves the newest choices.
		controller.preferredRetryAt = now.Add(time.Minute)
		log.Printf("agent: cannot save preferred servers: %v", err)
		return
	}
	controller.preferredSelections = saved
	controller.preferredRetryAt = time.Time{}
}
