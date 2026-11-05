// Adapted from SB Gateway 1.6.45 (AGPL-3.0).
package agentport

import (
	"errors"
	"time"
)

const sharedProbeLifetime = 15 * time.Second

var errSharedProbeDeferred = errors.New("shared probe already attempted in this cycle")

func (controller *healthController) deferSharedProbe(policyID string) {
	if controller.probeBudget != nil {
		if _, exists := controller.probeBudget.DeferredPolicies[policyID]; !exists {
			controller.probeBudget.DeferredPolicies[policyID] = time.Now()
		}
	}
}

type sharedProbeResult struct {
	At       time.Time
	Evidence probeEvidence
}

type parallelQualityRuntime interface {
	ProbeQualityParallel([]string) map[string]probeEvidence
}

// Sharing raw samples must not split a best-mode comparison across different
// observation windows. A missing peer cannot finish before now; an attempted
// but unavailable peer cannot be retried until the next ordinary budget.
func (controller *healthController) incoherentSharedComparison(policyID string, contract healthPolicyContract, nodes []string, now time.Time) bool {
	if contract.Mode != "best" || len(nodes) < 2 || controller.probeBudget == nil {
		return false
	}
	if qualityProbeWidth(controller.runtime) > 1 {
		cachedPeer, missingPeer, wave := false, false, uint64(0)
		for _, node := range nodes {
			key := controller.probeKey(policyID, node, contract)
			cached, ok := controller.probeBudget.Quality[key]
			if ok && now.Sub(cached.At) <= sharedProbeLifetime {
				id := cached.Evidence.QualityWave
				if isPairedPlanningMeasurement(controller.qualityMeasurementForPolicy(policyID, contract)) {
					id = 0
					if validPlanningQuality(cached.Evidence.planningQuality(), controller.qualityMeasurementForPolicy(policyID, contract)) {
						id = cached.Evidence.planningQuality().Wave
					}
				}
				if id == 0 || wave != 0 && wave != id {
					return true
				}
				cachedPeer, wave = true, id
			} else if controller.probeBudget.QualityAttempted[key] {
				return true
			} else {
				missingPeer = true
			}
		}
		if cachedPeer && missingPeer {
			return true
		}
	}
	var oldest, latest time.Time
	pending := false
	for _, node := range nodes {
		key := controller.probeKey(policyID, node, contract)
		cached, ok := controller.probeBudget.Quality[key]
		if ok && now.Sub(cached.At) <= sharedProbeLifetime {
			at := cached.Evidence.ObservedAt
			if isPairedPlanningMeasurement(controller.qualityMeasurementForPolicy(policyID, contract)) && validPlanningQuality(cached.Evidence.planningQuality(), controller.qualityMeasurementForPolicy(policyID, contract)) {
				at = cached.Evidence.planningQuality().ObservedAt
			}
			if at.IsZero() {
				at = cached.At
			}
			if oldest.IsZero() || at.Before(oldest) {
				oldest = at
			}
			if at.After(latest) {
				latest = at
			}
		} else if controller.probeBudget.QualityAttempted[key] {
			return true
		} else {
			pending = true
		}
	}
	if pending && now.After(latest) {
		latest = now
	}
	return !oldest.IsZero() && latest.Sub(oldest) > qualityPairWindow
}

func (controller *healthController) sharedQualityBatch(policyID string, contract healthPolicyContract, nodes []string) map[string]probeEvidence {
	result := make(map[string]probeEvidence, len(nodes))
	for _, node := range nodes {
		if cached, ok := controller.probeBudget.Quality[controller.probeKey(policyID, node, contract)]; ok && time.Since(cached.At) <= sharedProbeLifetime && controller.warmCachedBeforeConfirmation(policyID, node, cached.Evidence) {
			controller.deferSharedProbe(policyID)
			for _, id := range nodes {
				result[id] = probeEvidence{Deferred: true}
			}
			return result
		}
	}
	if controller.incoherentSharedComparison(policyID, contract, nodes, time.Now()) {
		controller.deferSharedProbe(policyID)
		for _, node := range nodes {
			result[node] = probeEvidence{Deferred: true}
		}
		return result
	}
	pending := []string{}
	for _, node := range nodes {
		key := controller.probeKey(policyID, node, contract)
		if cached, ok := controller.probeBudget.Quality[key]; ok && time.Since(cached.At) <= sharedProbeLifetime {
			result[node] = cached.Evidence
		} else if controller.probeBudget.QualityAttempted[key] {
			result[node] = probeEvidence{Deferred: true}
		} else {
			pending = append(pending, node)
		}
	}
	if runtime, ok := controller.runtime.(parallelQualityRuntime); ok && len(pending) > 1 {
		wave := uint64(0)
		if qualityProbeWidth(controller.runtime) > 1 {
			wave = qualityWaveSequence.Add(1)
		}
		for _, node := range pending {
			controller.probeBudget.QualityAttempted[controller.probeKey(policyID, node, contract)] = true
		}
		observations := map[string]probeEvidence{}
		if warm, ok := controller.runtime.(interface {
			ProbeQualityParallelWithMeasurement([]string, qualityMeasurement) map[string]probeEvidence
		}); ok {
			observations = warm.ProbeQualityParallelWithMeasurement(pending, controller.qualityMeasurementForPolicy(policyID, contract))
		} else {
			observations = runtime.ProbeQualityParallel(pending)
		}
		for node, evidence := range observations {
			if evidence.QualityWave == 0 {
				evidence.QualityWave = wave
			}
			result[node] = evidence
			if controller.cacheableQualityEvidence(policyID, node, contract, evidence) {
				controller.probeBudget.Quality[controller.probeKey(policyID, node, contract)] = sharedProbeResult{At: probeObservedAt(evidence), Evidence: evidence}
			}
		}
	} else {
		for _, node := range pending {
			result[node] = controller.sharedQualityProbe(policyID, node, contract)
		}
	}
	return result
}

func probeObservedAt(evidence probeEvidence) time.Time {
	if !evidence.ObservedAt.IsZero() {
		return evidence.ObservedAt
	}
	return time.Now()
}

func (controller *healthController) takeSharedProbeInterruption() error {
	err := takeProbeInterruption(controller.runtime)
	if err != nil && controller.probeBudget != nil {
		// Partial results cannot cross a generation change or policy transition.
		controller.probeBudget.Quality = map[string]sharedProbeResult{}
	}
	return err
}

// Only raw endpoint evidence is shared. Each policy keeps its own thresholds,
// history, priority order and switch confirmations.
type healthProbeBudget struct {
	Limit            int
	Used             map[string]bool
	Quality          map[string]sharedProbeResult
	Cursor           int
	QualityAttempted map[string]bool
	DeferredPolicies map[string]time.Time
}

func (controller *healthController) beginProbeBudget(pool healthPool, policyIDs []string) {
	if controller.probeBudget == nil {
		controller.probeBudget = &healthProbeBudget{Quality: map[string]sharedProbeResult{}}
	}
	budget := controller.probeBudget
	budget.Limit, budget.Used = pool.ProbeBudget, map[string]bool{}
	// A sample can serve different policies once, never another Tick's recovery.
	budget.Quality = map[string]sharedProbeResult{}
	budget.QualityAttempted = map[string]bool{}
	if budget.DeferredPolicies == nil {
		budget.DeferredPolicies = map[string]time.Time{}
	}
	for id := range budget.DeferredPolicies {
		if _, exists := pool.HealthPolicies[id]; !exists {
			delete(budget.DeferredPolicies, id)
		}
	}
	if len(policyIDs) > 1 {
		offset := budget.Cursor % len(policyIDs)
		ordered := append(append([]string{}, policyIDs[offset:]...), policyIDs[:offset]...)
		copy(policyIDs, ordered)
	}
}

func (controller *healthController) probeKey(policyID, node string, contract healthPolicyContract) string {
	if fingerprint := contract.Nodes[node].Fingerprint; fingerprint != "" {
		return string(controller.qualityMeasurementForPolicy(policyID, contract)) + ":" + controller.qualityContextKey() + ":" + fingerprint
	}
	// An unverified legacy endpoint cannot share evidence across policies.
	return string(controller.qualityMeasurementForPolicy(policyID, contract)) + ":" + controller.qualityContextKey() + ":" + policyID + ":" + node
}

func (controller *healthController) claimProbeTargets(policyID string, contract healthPolicyContract, targets []string, atomicPair bool) (accepted, deferred []string) {
	budget := controller.probeBudget
	if budget == nil || budget.Limit <= 0 {
		return targets, nil
	}
	now := time.Now()
	usable := func(key string) (cached, attempted bool) {
		result, ok := budget.Quality[key]
		return ok && now.Sub(result.At) <= sharedProbeLifetime, budget.QualityAttempted[key]
	}
	markDeferred := func() {
		if _, exists := budget.DeferredPolicies[policyID]; !exists {
			budget.DeferredPolicies[policyID] = now
		}
	}
	if controller.incoherentSharedComparison(policyID, contract, targets, now) {
		markDeferred()
		return nil, targets
	}
	newKeys := map[string]bool{}
	for _, node := range targets {
		key := controller.probeKey(policyID, node, contract)
		cached, attempted := usable(key)
		if atomicPair && attempted && !cached {
			markDeferred()
			return nil, targets
		}
		if !budget.Used[key] {
			newKeys[key] = true
		}
	}
	// Comparisons are indivisible: consuming half a pair can starve the next list.
	if atomicPair && len(newKeys)+len(budget.Used) > budget.Limit {
		markDeferred()
		return nil, targets
	}
	for _, node := range targets {
		key := controller.probeKey(policyID, node, contract)
		cached, attempted := usable(key)
		if !cached && attempted {
			deferred = append(deferred, node)
		} else if cached || budget.Used[key] || len(budget.Used) < budget.Limit {
			accepted = append(accepted, node)
			if !cached {
				budget.Used[key] = true
			}
		} else {
			deferred = append(deferred, node)
		}
	}
	if len(deferred) > 0 {
		markDeferred()
	}
	return accepted, deferred
}

func (controller *healthController) sharedQualityProbe(policyID, node string, contract healthPolicyContract) probeEvidence {
	budget := controller.probeBudget
	if budget == nil {
		return controller.probeQuality(policyID, node, contract)
	}
	key := controller.probeKey(policyID, node, contract)
	if result, ok := budget.Quality[key]; ok && time.Since(result.At) <= sharedProbeLifetime {
		if controller.warmCachedBeforeConfirmation(policyID, node, result.Evidence) {
			controller.deferSharedProbe(policyID)
			return probeEvidence{Deferred: true}
		}
		return result.Evidence
	}
	if budget.QualityAttempted[key] {
		return probeEvidence{Deferred: true}
	}
	budget.QualityAttempted[key] = true
	evidence := controller.probeQuality(policyID, node, contract)
	if controller.cacheableQualityEvidence(policyID, node, contract, evidence) {
		budget.Quality[key] = sharedProbeResult{At: probeObservedAt(evidence), Evidence: evidence}
	}
	return evidence
}

// An early shared result cannot move this policy's producer clock and postpone
// its next independent confirmation, even though its peer already paid the job.
func (controller *healthController) warmCachedBeforeConfirmation(policyID, node string, evidence probeEvidence) bool {
	item := controller.state[policyID]
	if item == nil || item.WarmPlanningMarker == "" || item.OptimizationChecks == 0 || item.OptimizationCandidate != node || item.OptimizationLastResult == nil || !validPlanningQuality(evidence.planningQuality(), qualityMeasurement(item.WarmPlanningMarker)) {
		return false
	}
	return float64(evidence.planningQuality().ObservedAt.UnixNano())/1e9 < item.OptimizationLastResult.CandidateAt+float64(item.ProbeLimits.ActiveSeconds)
}

func (controller *healthController) probeQuality(policyID, node string, contract healthPolicyContract) probeEvidence {
	if r, ok := controller.runtime.(interface {
		ProbeWithQualityRequest(string, qualityProbeRequest) probeEvidence
	}); ok {
		return r.ProbeWithQualityRequest(node, qualityProbeRequest{Measurement: controller.qualityMeasurementForPolicy(policyID, contract)})
	}
	return controller.runtime.Probe(node)
}
func (controller *healthController) cacheableQualityEvidence(policyID, node string, contract healthPolicyContract, evidence probeEvidence) bool {
	return evidence.OK && contract.Nodes[node].Fingerprint != "" && (!isPairedPlanningMeasurement(controller.qualityMeasurementForPolicy(policyID, contract)) || validPlanningQuality(evidence.planningQuality(), controller.qualityMeasurementForPolicy(policyID, contract)))
}
