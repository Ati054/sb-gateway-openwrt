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
	var oldest, latest time.Time
	pending := false
	for _, node := range nodes {
		key := controller.probeKey(policyID, node, contract)
		cached, ok := controller.probeBudget.Quality[key]
		if ok && now.Sub(cached.At) <= sharedProbeLifetime {
			at := cached.Evidence.ObservedAt
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
		for _, node := range pending {
			controller.probeBudget.QualityAttempted[controller.probeKey(policyID, node, contract)] = true
		}
		for node, evidence := range runtime.ProbeQualityParallel(pending) {
			result[node] = evidence
			if evidence.OK && contract.Nodes[node].Fingerprint != "" {
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
		return fingerprint
	}
	// An unverified legacy endpoint cannot share evidence across policies.
	return policyID + ":" + node
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
		return controller.runtime.Probe(node)
	}
	key := controller.probeKey(policyID, node, contract)
	if result, ok := budget.Quality[key]; ok && time.Since(result.At) <= sharedProbeLifetime {
		return result.Evidence
	}
	if budget.QualityAttempted[key] {
		return probeEvidence{Deferred: true}
	}
	budget.QualityAttempted[key] = true
	evidence := controller.runtime.Probe(node)
	if evidence.OK && contract.Nodes[node].Fingerprint != "" {
		budget.Quality[key] = sharedProbeResult{At: probeObservedAt(evidence), Evidence: evidence}
	}
	return evidence
}
