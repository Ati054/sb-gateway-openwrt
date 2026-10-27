// Adapted from SB Gateway 1.6.45 (AGPL-3.0).
package agentport

import (
	"sort"
	"time"
)

type probeFailureClass string

const (
	probeFailureNone      probeFailureClass = ""
	probeFailureTimeout   probeFailureClass = "timeout"
	probeFailureFatal     probeFailureClass = "fatal-network"
	probeFailureTLS       probeFailureClass = "fatal-tls"
	probeFailureDNS       probeFailureClass = "dns"
	probeFailureTransient probeFailureClass = "transient"
)

type underlayEvidence struct {
	Known bool
	WANOK bool
	DNSOK bool
}

type underlayRuntime interface {
	UnderlayStatus() underlayEvidence
}

type parallelAvailabilityRuntime interface {
	ProbeAvailabilityParallel([]string, func(string, probeEvidence) bool) map[string]probeEvidence
}

type emergencyAvailabilityRuntime interface {
	ProbeEmergencyAvailabilityParallel([]string, func(string, probeEvidence) bool) map[string]probeEvidence
}

type activeConfirmationRuntime interface {
	ProbeAvailabilityConfirmation(string) probeEvidence
}

type emergencyCandidateRuntime interface {
	ProbeEmergencyAvailability(string) probeEvidence
}

func failureConfirmationThreshold(evidence probeEvidence, configured int) int {
	switch evidence.Failure {
	case probeFailureFatal, probeFailureTLS:
		return 1
	case probeFailureTimeout:
		return minInt(configured, 2)
	default:
		return configured
	}
}

func startupFailureConfirmationThreshold(evidence probeEvidence, configured int, warm bool) int {
	threshold := failureConfirmationThreshold(evidence, configured)
	if warm && evidence.Failure != probeFailureTLS && configured > 1 && threshold < 2 {
		// The first active probe after a core restart can race the newly restored
		// dynamic handler and briefly report connection refused/no route. Preserve
		// the confirmed startup selection until one independent fast-lane probe
		// repeats that network failure. A TLS failure remains deterministic and
		// therefore keeps its immediate failover semantics.
		return 2
	}
	return threshold
}

func (controller *healthController) suppressForUnderlayFailure(now time.Time, evidence probeEvidence, item *policyHealthState) bool {
	// A certificate or protocol failure belongs to the selected path. Checking
	// the shared WAN first would only delay a deterministic failover.
	if evidence.Failure == probeFailureTLS {
		item.UnderlayFailure = ""
		item.UnderlayCheckedAt = ""
		return false
	}
	probe, ok := controller.runtime.(underlayRuntime)
	if !ok {
		return false
	}
	status := probe.UnderlayStatus()
	if !status.Known {
		return false
	}
	item.UnderlayCheckedAt = now.UTC().Format(time.RFC3339Nano)
	switch {
	case !status.WANOK:
		item.UnderlayFailure = "wan"
		return true
	case !status.DNSOK:
		item.UnderlayFailure = "dns"
		return true
	default:
		item.UnderlayFailure = ""
		return false
	}
}

func clearUnderlayFailure(item *policyHealthState) bool {
	changed := item.UnderlayFailure != "" || item.UnderlayCheckedAt != ""
	item.UnderlayFailure = ""
	item.UnderlayCheckedAt = ""
	return changed
}

// Cached quality orders the current availability checks; old availability and
// recovery counters do not authorize or disqualify an emergency selection.
func freshEmergencyReserves(now time.Time, selected string, candidates []string, mode string, groups map[string]int, item *policyHealthState, p effectivePolicySettings) []string {
	if item == nil {
		return nil
	}
	fresh := make([]string, 0, len(candidates))
	delays := make(map[string]*int)
	for _, candidate := range candidates {
		age, valid := qualitySampleAge(now, item, candidate)
		if candidate == selected || !valid || age > float64(p.backup) {
			continue
		}
		fresh = append(fresh, candidate)
		samples := item.Samples[candidate]
		delays[candidate] = samples[len(samples)-1].DelayMS
	}
	return rankCandidates(fresh, mode, groups, nil, delays)
}

// Emergency preference uses recent primary HTTPS quality. Recovery streaks and
// smoothed quality govern planned changes; a current availability success is
// still required separately before an emergency candidate can be selected.
func emergencyRankingDelays(now time.Time, candidates []string, measured map[string]probeEvidence, item *policyHealthState, p effectivePolicySettings) map[string]*int {
	delays := make(map[string]*int, len(candidates))
	for _, candidate := range candidates {
		evidence, current := measured[candidate]
		if current && evidence.PrimaryQualityFailed {
			continue
		}
		if current && evidence.OK && !evidence.QualityUnmeasured && evidence.DelayMS != nil && *evidence.DelayMS > 0 {
			at := qualityTime(now, evidence)
			if !at.After(now) && now.Sub(at) <= time.Duration(p.backup)*time.Second {
				delays[candidate] = evidence.DelayMS
				continue
			}
		}
		if age, valid := qualitySampleAge(now, item, candidate); valid && age <= float64(p.backup) {
			delays[candidate] = item.Samples[candidate][len(item.Samples[candidate])-1].DelayMS
		}
	}
	return delays
}

func knownFreshReserve(now time.Time, selected string, candidates []string, mode string, groups map[string]int, item *policyHealthState, p effectivePolicySettings) string {
	fresh := freshEmergencyReserves(now, selected, candidates, mode, groups, item, p)
	if len(fresh) == 0 {
		return ""
	}
	return fresh[0]
}

// A cached reserve gets the first emergency slot, but must pass a current
// availability probe before Select. The configured batch size still bounds
// work even when several reserves fail together.
func prioritizeEmergencyReserve(targets []string, reserve string, closed map[string]bool, batch int) []string {
	if reserve == "" || closed[reserve] || batch <= 0 {
		return targets
	}
	result := []string{reserve}
	for _, target := range targets {
		if len(result) >= batch {
			break
		}
		if target != reserve {
			result = append(result, target)
		}
	}
	return result
}

func (controller *healthController) probeEmergencyCandidates(now time.Time, policyID string, candidates []string, p effectivePolicySettings) (observations map[string]probeEvidence, chosen string, probeErr error) {
	preferred := []string(nil)
	if p.mode == "best" {
		item := controller.state[policyID]
		selected := ""
		if item != nil {
			selected = item.Selected
		}
		preferred = freshEmergencyReserves(now, selected, candidates, p.mode, nil, item, p)
		// Start qualified reserves first even when the batch has fewer lanes.
		candidates = uniqueCandidates(append(append([]string(nil), preferred...), candidates...))
	}
	if p.mode == "priority" {
		candidates = append([]string(nil), candidates...)
		if order := controller.processedPool.HealthPolicies[policyID].Candidates; len(order) > 0 {
			position := make(map[string]int, len(order))
			for i, id := range order {
				position[id] = i
			}
			sort.SliceStable(candidates, func(i, j int) bool { return position[candidates[i]] < position[candidates[j]] })
		}
	}
	trace := beginHealthStage("emergency_batch", "controller", policyID, "")
	defer func() { trace.finish(probeErr == nil && chosen != "") }()
	measured := make(map[string]probeEvidence, len(candidates))
	selected := ""
	var selectErr error
	completed := make(map[string]probeEvidence, len(candidates))
	accept := func(candidate string, evidence probeEvidence) bool {
		completed[candidate] = evidence
		if selected != "" || selectErr != nil {
			return false
		}
		wanted := candidate
		if p.mode == "priority" {
			wanted = ""
			for _, earlier := range candidates {
				result, done := completed[earlier]
				if !done {
					return false
				}
				if result.LocalFailure {
					selectErr = errProbeSelectorUnavailable
					return false
				}
				if result.OK {
					wanted = earlier
					break
				}
			}
		}
		if p.mode == "best" && len(preferred) > 0 {
			// An earlier result cannot steal selection from a faster qualified
			// reserve. Each emergency probe has bounded origin deadlines; a
			// failed preferred path releases already completed alternatives.
			wanted = ""
			for _, reserve := range preferred {
				result, done := completed[reserve]
				if !done {
					return false
				}
				if result.LocalFailure {
					selectErr = errProbeSelectorUnavailable
					return false
				}
				if result.OK {
					wanted = reserve
					break
				}
			}
			if wanted == "" {
				for _, alternative := range candidates {
					if result := completed[alternative]; result.OK && !result.LocalFailure {
						wanted = alternative
						break
					}
				}
			}
		}
		if wanted == "" || !completed[wanted].OK || completed[wanted].LocalFailure {
			return false
		}
		if err := controller.runtime.Select(policyID, wanted); err != nil {
			selectErr = err
			return false
		}
		selected = wanted
		return true
	}

	if urgent, ok := controller.runtime.(emergencyAvailabilityRuntime); ok && len(candidates) > 0 {
		measured = urgent.ProbeEmergencyAvailabilityParallel(candidates, accept)
	} else if parallel, ok := controller.runtime.(parallelAvailabilityRuntime); ok && len(candidates) > 1 {
		measured = parallel.ProbeAvailabilityParallel(candidates, accept)
	} else {
		for _, candidate := range candidates {
			evidence := controller.runtime.ProbeAvailability(candidate)
			if evidence.LocalFailure {
				return measured, selected, errProbeSelectorUnavailable
			}
			measured[candidate] = evidence
			if accept(candidate, evidence) {
				break
			}
		}
	}
	if err := takeProbeInterruption(controller.runtime); err != nil {
		return measured, selected, err
	}
	return measured, selected, selectErr
}
