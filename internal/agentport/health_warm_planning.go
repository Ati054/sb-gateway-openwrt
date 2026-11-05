package agentport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// The opt-in affects ordinary best-mode surveys only. Startup and emergency
// entry points keep their existing cold request contract.
func (controller *healthController) qualityMeasurementForPolicy(policyID string, contract healthPolicyContract) qualityMeasurement {
	if isPairedPlanningMeasurement(controller.opts.planningMeasurement()) && contract.Mode == "best" && controller.warmStarted[policyID] {
		return controller.opts.planningMeasurement()
	}
	return qualityMeasurementCold
}

func (controller *healthController) qualityContextKey() string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%s\n%d\n%d\n%s", controller.opts.planningMeasurement(), controller.opts.ProbeURL, healthTargets[0].url,
		healthTargets[0].status, controller.processedGeneration.XrayPID, controller.processedGeneration.PoolSHA256)))
	return hex.EncodeToString(digest[:])
}

func (controller *healthController) prepareWarmPlanning(contract healthPolicyContract, item *policyHealthState) {
	if !isPairedPlanningMeasurement(controller.opts.planningMeasurement()) || contract.Mode != "best" {
		resetWarmPlanning(item)
		return
	}
	hash := sha256.New()
	fmt.Fprintln(hash, controller.qualityContextKey())
	fmt.Fprintln(hash, policySettings(contract.Policy, contract.Mode).active)
	for _, node := range uniqueCandidates(contract.Candidates) {
		fmt.Fprintln(hash, node, contract.Nodes[node].Fingerprint)
	}
	signature := hex.EncodeToString(hash.Sum(nil))
	if item.WarmPlanningMarker == string(controller.opts.planningMeasurement()) && item.WarmPlanningSignature == signature && item.warmPlanningLatest != nil && item.warmPlanningCountedAt != nil {
		return
	}
	resetWarmPlanning(item)
	clearOptimizationCandidate(item)
	item.OptimizationLastResult, item.displayComparisons = nil, nil
	item.WarmPlanningMarker, item.WarmPlanningSignature = string(controller.opts.planningMeasurement()), signature
	item.WarmPlanningSamples = make(map[string][]healthSample)
	item.WarmPlanningLastProbeAt = make(map[string]float64)
	item.warmPlanningLatest = make(map[string]warmQualityEvidence)
	item.warmPlanningCountedAt = make(map[string]time.Time)
}

func resetWarmPlanning(item *policyHealthState) {
	if item == nil {
		return
	}
	if item.WarmPlanningMarker == "" {
		return
	}
	item.qualityPair = nil
	item.WarmPlanningMarker, item.WarmPlanningSignature = "", ""
	item.WarmPlanningSamples, item.WarmPlanningLastProbeAt, item.warmPlanningLatest = nil, nil, nil
	item.warmPlanningCountedAt = nil
	clearOptimizationCandidate(item)
	item.OptimizationLastResult, item.LatencyComparisons, item.displayComparisons = nil, nil, nil
}

func validWarmQuality(value *warmQualityEvidence) bool {
	return validPlanningQuality(value, qualityMeasurementWarm)
}

func invalidateWarmNode(item *policyHealthState, node string) {
	if pair := item.qualityPair; pair != nil && (node == pair.Active || node == pair.Candidate) {
		item.qualityPair = nil
	}
	delete(item.WarmPlanningSamples, node)
	delete(item.warmPlanningLatest, node)
	delete(item.LatencyComparisons, node)
	if node == item.Selected {
		item.LatencyComparisons = nil
	}
	// A failed/unknown probe withdraws planning evidence, not the last qualified
	// display value. Endpoint, metric, membership and active-route changes have
	// their own invalidation paths; display history never confirms a switch.
	if node == item.Selected || node == item.OptimizationCandidate {
		clearOptimizationCandidate(item)
		item.OptimizationLastResult = nil
	}
}

// Fold only nested warm observations into a bounded planning history. Cold
// counters, samples and Daily/30-day statistics are never written here.
func collectWarmPlanning(now time.Time, candidates []string, measured map[string]probeEvidence, item *policyHealthState, p effectivePolicySettings) {
	if !isPairedPlanningMeasurement(qualityMeasurement(item.WarmPlanningMarker)) {
		return
	}
	for _, node := range candidates {
		if evidence, exists := measured[node]; exists {
			if evidence.planningQuality() == nil && evidence.OK && evidence.QualityUnmeasured {
				continue // Foreground availability is not a failed warm job.
			}
			attemptAt := qualityTime(now, evidence)
			if evidence.planningQuality() != nil && evidence.planningQuality().ObservedAt.After(attemptAt) {
				attemptAt = evidence.planningQuality().ObservedAt
			}
			// Scheduling uses whole seconds. Round completion up so a changed
			// request duration cannot bring the next attempt before its interval.
			attemptSecond := attemptAt.Unix()
			if attemptAt.Nanosecond() != 0 {
				attemptSecond++
			}
			item.WarmPlanningLastProbeAt[node] = max(item.WarmPlanningLastProbeAt[node], float64(attemptSecond))
			if evidence.OK && !evidence.PrimaryQualityFailed && evidence.planningQuality() != nil && evidence.planningQuality().Deferred && evidence.planningQuality().StartedAt.IsZero() {
				continue // No quality observation happened; preserve its watermark.
			}
			at := qualityTime(now, evidence)
			if evidence.planningQuality() != nil && !evidence.planningQuality().ObservedAt.IsZero() {
				at = evidence.planningQuality().ObservedAt
			}
			if previous, exists := item.warmPlanningCountedAt[node]; exists && !at.After(previous) {
				continue // Neither success nor unknown can overwrite a newer result.
			}
			item.warmPlanningCountedAt[node] = at
			if !evidence.OK || evidence.PrimaryQualityFailed || evidence.QualityUnmeasured || !validPlanningQuality(evidence.planningQuality(), qualityMeasurement(item.WarmPlanningMarker)) {
				invalidateWarmNode(item, node)
				continue
			}
			value := *evidence.planningQuality()
			valueMS := *value.DelayMS
			value.DelayMS = &valueMS
			if previous, exists := item.warmPlanningLatest[node]; exists && !value.ObservedAt.After(previous.ObservedAt) {
				continue // Shared or late evidence never becomes a second sample.
			}
			item.warmPlanningLatest[node] = value
			sample := healthSample{At: float64(value.ObservedAt.UnixNano()) / 1e9, OK: true, DelayMS: value.DelayMS}
			item.WarmPlanningSamples[node] = appendQualityHistory(now, item.WarmPlanningSamples[node], sample, p)
		} else if samples, exists := item.WarmPlanningSamples[node]; exists {
			samples = recentQualitySamples(now, samples, p)
			if len(samples) == 0 {
				delete(item.WarmPlanningSamples, node)
				delete(item.warmPlanningLatest, node)
			} else {
				item.WarmPlanningSamples[node] = samples
			}
		}
	}
}

func warmPlanningView(item *policyHealthState) *policyHealthState {
	if item == nil || !isPairedPlanningMeasurement(qualityMeasurement(item.WarmPlanningMarker)) {
		return item
	}
	view := *item
	view.planningSampleView = true
	view.Samples = item.WarmPlanningSamples
	view.LastProbeAt = item.WarmPlanningLastProbeAt
	view.QualityOK = make(map[string]bool, len(item.QualityOK))
	for node, samples := range view.Samples {
		view.QualityOK[node] = len(samples) > 0 && samples[len(samples)-1].OK && samples[len(samples)-1].DelayMS != nil
	}
	return &view
}

func warmPlanningMeasured(measured map[string]probeEvidence, item *policyHealthState) map[string]probeEvidence {
	if !isPairedPlanningMeasurement(qualityMeasurement(item.WarmPlanningMarker)) {
		return measured
	}
	result := make(map[string]probeEvidence, len(measured))
	for node, cold := range measured {
		if cold.OK && !cold.PrimaryQualityFailed && !cold.QualityUnmeasured && validPlanningQuality(cold.planningQuality(), qualityMeasurement(item.WarmPlanningMarker)) {
			value := cold.planningQuality()
			if latest, exists := item.warmPlanningLatest[node]; !exists || !value.ObservedAt.Equal(latest.ObservedAt) {
				continue
			}
			result[node] = probeEvidence{OK: true, DelayMS: value.DelayMS, QualityStartedAt: value.StartedAt,
				ObservedAt: value.ObservedAt, QualityWave: value.Wave}
		}
	}
	return result
}

// History alone carries no wave identity. A cached half is usable only while
// it is still the exact qualified observation kept by this planning session.
func cachedWarmPairEvidence(now time.Time, node string, item *policyHealthState) (probeEvidence, bool) {
	warm, exists := item.warmPlanningLatest[node]
	if !exists || !validPlanningQuality(&warm, qualityMeasurement(item.WarmPlanningMarker)) || warm.ObservedAt.After(now) {
		return probeEvidence{}, false
	}
	samples := item.Samples[node]
	if len(samples) == 0 || !goodPlanningSample(samples[len(samples)-1]) || samples[len(samples)-1].At <= 0 {
		return probeEvidence{}, false
	}
	if processed, known := item.warmPlanningCountedAt[node]; !known || !processed.Equal(warm.ObservedAt) {
		return probeEvidence{}, false
	}
	return probeEvidence{OK: true, DelayMS: warm.DelayMS, QualityStartedAt: warm.StartedAt,
		ObservedAt: warm.ObservedAt, QualityWave: warm.Wave}, true
}

func planningComparison(now time.Time, selected, candidate string, measured map[string]probeEvidence, item *policyHealthState, quality, available bool, p effectivePolicySettings) *optimizationComparison {
	if item.WarmPlanningMarker == "" {
		return regularOptimizationComparison(now, selected, candidate, measured, item, quality, available, p)
	}
	view := warmPlanningView(item)
	comparison := regularOptimizationComparison(now, selected, candidate, warmPlanningMeasured(measured, item), view, quality && view.QualityOK[candidate], available, p)
	if comparison != nil && item.WarmPlanningMarker != "" {
		comparison.Measurement = item.WarmPlanningMarker
	}
	return comparison
}

func warmPlanningScheduleView(item *policyHealthState) *policyHealthState {
	if !isPairedPlanningMeasurement(qualityMeasurement(item.WarmPlanningMarker)) || primaryDegradationPending(item, item.Selected) {
		return item // The independent cold degraded-pair repair retains priority.
	}
	view := *item
	view.planningSampleView = true
	view.Samples, view.LastProbeAt = item.WarmPlanningSamples, item.WarmPlanningLastProbeAt
	return &view
}

func recentQualitySamples(now time.Time, samples []healthSample, p effectivePolicySettings) []healthSample {
	result := make([]healthSample, 0, 3)
	window := planningHistoryWindow(p)
	for _, sample := range samples {
		age := float64(now.UnixNano())/1e9 - sample.At
		if age >= 0 && age <= window {
			result = append(result, sample)
		}
	}
	if len(result) > 3 {
		result = append([]healthSample(nil), result[len(result)-3:]...)
	}
	return result
}
