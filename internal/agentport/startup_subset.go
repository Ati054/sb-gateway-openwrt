package agentport

import (
	"slices"
	"strings"
)

type workingSelection struct {
	Selected           string `json:"selected"`
	Mode               string `json:"mode"`
	CandidateSignature string `json:"candidate_signature"`
}

// Membership edits do not reorder the surviving priority queue.
func priorityOrderChanged(signature string, candidates []string) bool {
	if signature == "" {
		return true
	}
	previous := uniqueCandidates(strings.Split(signature, "\n"))
	oldSet, newSet := make(map[string]bool, len(previous)), make(map[string]bool, len(candidates))
	for _, node := range previous {
		oldSet[node] = true
	}
	for _, node := range candidates {
		newSet[node] = true
	}
	oldCommon, newCommon := []string{}, []string{}
	for _, node := range previous {
		if newSet[node] {
			oldCommon = append(oldCommon, node)
		}
	}
	for _, node := range uniqueCandidates(candidates) {
		if oldSet[node] {
			newCommon = append(newCommon, node)
		}
	}
	return !slices.Equal(oldCommon, newCommon)
}

// Members of one location share priority. Subscription reordering inside a
// location must not withdraw a working member on reload or restart.
func priorityGroupOrderChanged(signature string, candidates []string, groups map[string]int) bool {
	if signature == "" {
		return true
	}
	if !priorityOrderChanged(signature, candidates) {
		return false
	}
	previous := uniqueCandidates(strings.Split(signature, "\n"))
	oldSet, newSet := make(map[string]bool, len(previous)), make(map[string]bool, len(candidates))
	for _, node := range previous {
		oldSet[node] = true
	}
	for _, node := range candidates {
		newSet[node] = true
	}
	oldCommon, newCommon := []int{}, []int{}
	for _, node := range previous {
		if newSet[node] {
			group, ok := groups[node]
			if !ok {
				return true
			}
			oldCommon = append(oldCommon, group)
		}
	}
	for _, node := range uniqueCandidates(candidates) {
		if oldSet[node] {
			group, ok := groups[node]
			if !ok {
				return true
			}
			newCommon = append(newCommon, group)
		}
	}
	return !slices.Equal(oldCommon, newCommon)
}

func rememberWorkingSelection(contract healthPolicyContract, item *policyHealthState) bool {
	if !item.RuntimeConfirmed || item.RuntimeSelected != item.Selected ||
		!item.AvailabilityOK[item.Selected] || item.Selected == "block" ||
		item.AvailabilityFailures[item.Selected] >= policySettings(contract.Policy, contract.Mode).failureThreshold ||
		!contains(contract.Candidates, item.Selected) || item.Mode != contract.Mode ||
		item.CandidateSignature != strings.Join(uniqueCandidates(contract.Candidates), "\n") {
		return false
	}
	value := workingSelection{item.Selected, item.Mode, item.CandidateSignature}
	if item.LastWorkingSelection != nil && *item.LastWorkingSelection == value {
		return false
	}
	item.LastWorkingSelection = &value
	return true
}

// A restart of a different sheet must not turn this sheet into a new race.
// The saved handler is admitted only after a fresh isolated availability check.
func restorableWorkingSelection(contract healthPolicyContract, item *policyHealthState) bool {
	saved := savedWorkingSelection(item)
	if saved == nil || saved.Mode != contract.Mode || !contains(contract.Candidates, saved.Selected) ||
		item.AvailabilityFailures[saved.Selected] >= policySettings(contract.Policy, contract.Mode).failureThreshold {
		return false
	}
	if contract.Mode == "priority" {
		groups, _, _ := candidateGroups(contract.Groups, contract.Candidates, contract.Mode)
		if priorityGroupOrderChanged(saved.CandidateSignature, contract.Candidates, groups) {
			return false
		}
		// Repair a former cold-start race; this is not a confirmed outage/recovery.
		if item.LastSwitchReason == "fresh-path-available" {
			for _, candidate := range contract.Candidates {
				if candidate == saved.Selected {
					break
				}
				if groups[candidate] < groups[saved.Selected] && item.AvailabilityOK[candidate] && item.AvailabilityFailures[candidate] == 0 {
					return false
				}
			}
		}
	}
	return true
}

// A confirmed observation can be newer than the durable last-working record.
// An unprobed or changed endpoint must never be admitted by readback alone.
func savedWorkingSelection(item *policyHealthState) *workingSelection {
	if item.RuntimeConfirmed && item.RuntimeSelected == item.Selected && item.Selected != "block" &&
		(item.AvailabilityOK[item.Selected] || item.LastGoodAt[item.Selected] > 0) {
		return &workingSelection{item.Selected, item.Mode, item.CandidateSignature}
	}
	return item.LastWorkingSelection
}
