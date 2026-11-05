package agentport

import (
	"fmt"
	"math"
	"time"
)

const qualityPairWindow = 10 * time.Second

// A continuation consumes an ordinary slot in the next shared budget. It is
// attempted once; a slow or expired pair returns to the regular schedule.
type qualityPairContinuation struct {
	Active, Node, Candidate        string
	StartedAt                      time.Time
	Measurement                    qualityMeasurement
	Context, ActiveFP, CandidateFP string
}

func qualityTimestamp(now time.Time, evidence probeEvidence) float64 {
	if !evidence.ObservedAt.IsZero() {
		return float64(evidence.ObservedAt.UnixNano()) / 1e9
	}
	return float64(now.UnixNano()) / 1e9
}

func latestQuality(item *policyHealthState, node string) (healthSample, bool) {
	sample := latestQualityObservation(item, node)
	return sample, sample.OK && sample.DelayMS != nil && *sample.DelayMS > 0 && sample.At > 0
}

func closeQualityPair(item *policyHealthState, active, candidate string) bool {
	a, goodA := latestQuality(item, active)
	b, goodB := latestQuality(item, candidate)
	return goodA && goodB && math.Abs(a.At-b.At) <= qualityPairWindow.Seconds()
}

func primaryDegradationPending(item *policyHealthState, active string) bool {
	sample := latestQualityObservation(item, active)
	return item.Failures[active] > 0 && sample.OK && sample.PrimaryQualityFailed && sample.At > 0
}

// Only a response from the primary itself clears its degradation streak;
// availability through another origin says nothing about primary recovery.
func primaryOriginAvailable(evidence probeEvidence) bool {
	if !evidence.OK || len(healthTargets) == 0 {
		return false
	}
	label := healthTargets[0].label
	delay := evidence.Targets[label]
	_, failed := evidence.TargetFailures[label]
	return delay != nil && *delay > 0 && !failed
}

// Missing HEAD timing must not turn a reachable route into an emergency.
// Old snapshots have no primary-failure marker and cannot authorize a switch.
func closeDegradedPair(now time.Time, item *policyHealthState, active, candidate string) bool {
	if !primaryDegradationPending(item, active) {
		return false
	}
	a := latestQualityObservation(item, active)
	b, good := latestQuality(item, candidate)
	at := float64(now.UnixNano()) / 1e9
	return good && !b.PrimaryQualityFailed && at >= a.At && at >= b.At &&
		at-a.At <= qualityPairWindow.Seconds() && at-b.At <= qualityPairWindow.Seconds() &&
		math.Abs(a.At-b.At) <= qualityPairWindow.Seconds()
}

func recentQualityMedian(now time.Time, samples []healthSample, p effectivePolicySettings) *int {
	if len(samples) == 0 || !samples[len(samples)-1].OK || samples[len(samples)-1].DelayMS == nil {
		return nil
	}
	window := planningHistoryWindow(p)
	delays := make([]int, 0, 3)
	for i := len(samples) - 1; i >= 0 && len(delays) < 3; i-- {
		sample := samples[i]
		age := float64(now.UnixNano())/1e9 - sample.At
		// A failed primary measurement ends the smoothing window.
		if !sample.OK || sample.DelayMS == nil {
			break
		}
		if age >= 0 && age <= window && *sample.DelayMS > 0 {
			delays = append(delays, *sample.DelayMS)
			if p.mode == "priority" {
				break
			}
		}
	}
	if len(delays) == 0 {
		return nil
	}
	value := medianInt(delays)
	return &value
}

func pairContinuationValid(now time.Time, item *policyHealthState) bool {
	if item == nil {
		return false
	}
	pair := item.qualityPair
	if pair != nil && pair.Context != "" {
		measurement := qualityMeasurementCold
		if item.WarmPlanningMarker != "" && !primaryDegradationPending(item, item.Selected) {
			measurement = qualityMeasurement(item.WarmPlanningMarker)
		}
		if pair.Measurement != measurement || pair.Context != displayQualityContext(item) ||
			pair.ActiveFP != item.CandidateNodes[pair.Active].Fingerprint || pair.CandidateFP != item.CandidateNodes[pair.Candidate].Fingerprint ||
			!item.RuntimeConfirmed || item.UnderlayFailure != "" || item.AvailabilityFailures[item.Selected] > 0 {
			return false
		}
		if _, exists := item.CandidateLabels[pair.Candidate]; !exists {
			return false
		}
	}
	return pair != nil && pair.Active == item.Selected &&
		!now.Before(pair.StartedAt) && now.Sub(pair.StartedAt) <= qualityPairWindow
}

// Refresh the baseline within a large sweep, using the same batch and global
// request budgets. With one slot, the reserved peer follows on the next wake.
func qualityBatchTargets(now time.Time, selected string, targets []string, item *policyHealthState, p effectivePolicySettings, concurrent bool) []string {
	original := item
	item = warmPlanningScheduleView(item)
	defer func() { original.qualityPair = item.qualityPair }()

	if pairContinuationValid(now, item) {
		if concurrent && p.mode == "best" && p.batch > 1 {
			return []string{selected, item.qualityPair.Candidate}
		}
		return []string{item.qualityPair.Node}
	}
	item.qualityPair = nil
	if len(targets) > p.batch {
		targets = targets[:p.batch]
	}
	if len(targets) == 0 || contains(targets, selected) {
		return targets
	}
	if concurrent && p.mode == "best" && p.batch > 1 {
		return append([]string{selected}, targets...)[:minInt(len(targets)+1, p.batch)]
	}
	active, good := latestQuality(item, selected)
	age := float64(now.UnixNano())/1e9 - active.At
	if good && age >= 0 && age < qualityPairWindow.Seconds()/2 {
		return targets
	}
	return append([]string{selected}, targets...)[:minInt(len(targets)+1, p.batch)]
}

func planQualityContinuation(now time.Time, selected string, intended, targets []string, measured map[string]probeEvidence, item *policyHealthState, continuing bool) {
	original := item
	item = warmPlanningScheduleView(item)
	warm := item.WarmPlanningMarker != "" && !primaryDegradationPending(original, selected)
	defer func() { original.qualityPair = item.qualityPair; original.qualityPeer = item.qualityPeer }()
	if warm {
		if pair := item.qualityPair; continuing && pair != nil {
			if e, attempted := measured[pair.Node]; attempted && !e.Deferred && e.planningQuality() != nil && !validPlanningQuality(e.planningQuality(), qualityMeasurement(item.WarmPlanningMarker)) {
				item.qualityPair = nil
				return
			}
		}
		measured = warmPlanningMeasured(measured, item)
	}

	if continuing {
		// A deferred slot can be retried while the original pair is still fresh.
		if pair := item.qualityPair; pair != nil {
			if _, attempted := measured[pair.Node]; attempted || !pairContinuationValid(now, item) {
				if attempted && (closeQualityPair(item, selected, pair.Candidate) || closeDegradedPair(now, item, selected, pair.Candidate)) {
					item.qualityPeer = pair.Candidate
				}
				item.qualityPair = nil
			}
		}
		return
	}
	for _, node := range intended {
		if node != selected && !contains(targets, node) {
			if evidence, ok := measured[selected]; ok && evidence.OK && !evidence.LocalFailure {
				item.qualityPair = newQualityContinuation(selected, node, node, qualityTime(now, evidence), original, warm)
				return
			}
		}
	}
	for _, node := range targets {
		if evidence, ok := measured[node]; node != selected && ok && evidence.OK && evidence.DelayMS != nil &&
			!closeQualityPair(item, selected, node) && !closeDegradedPair(now, item, selected, node) {
			item.qualityPair = newQualityContinuation(selected, selected, node, qualityTime(now, evidence), original, warm)
			return
		}
	}
}

func newQualityContinuation(active, node, candidate string, at time.Time, item *policyHealthState, warm bool) *qualityPairContinuation {
	measurement := qualityMeasurementCold
	if warm {
		measurement = qualityMeasurement(item.WarmPlanningMarker)
	}
	return &qualityPairContinuation{Active: active, Node: node, Candidate: candidate, StartedAt: at, Measurement: measurement,
		Context: displayQualityContext(item), ActiveFP: item.CandidateNodes[active].Fingerprint, CandidateFP: item.CandidateNodes[candidate].Fingerprint}
}

func qualityTime(now time.Time, evidence probeEvidence) time.Time {
	if !evidence.ObservedAt.IsZero() {
		return evidence.ObservedAt
	}
	return now
}

func qualityEvaluationTime(now time.Time, measured map[string]probeEvidence) time.Time {
	for _, evidence := range measured {
		if evidence.planningQuality() != nil && evidence.planningQuality().ObservedAt.After(now) {
			now = evidence.planningQuality().ObservedAt
		}
		if evidence.ObservedAt.After(now) {
			now = evidence.ObservedAt
		}
	}
	return now
}

type displayLatencyComparison struct {
	Pair                           latencyComparison
	Context, ActiveFP, CandidateFP string
}

func displayQualityContext(item *policyHealthState) string {
	return fmt.Sprintf("%s|%s|%s|%v", item.LatencyMeasurement, item.WarmPlanningMarker, item.WarmPlanningSignature, healthTargets)
}

// This cache is only presentation data. Selection never reads it.
func refreshDisplayComparisons(now time.Time, item *policyHealthState) {
	active := item.RuntimeSelected
	context := displayQualityContext(item)
	for id, saved := range item.displayComparisons {
		_, exists := item.CandidateLabels[id]
		if !exists || saved.Pair.Active != active || saved.Context != context ||
			saved.ActiveFP != item.CandidateNodes[active].Fingerprint || saved.CandidateFP != item.CandidateNodes[id].Fingerprint {
			delete(item.displayComparisons, id)
		}
	}
	if !item.RuntimeConfirmed || active != item.Selected || active == "" || active == "block" || !item.AvailabilityOK[active] || item.AvailabilityFailures[active] > 0 {
		return
	}
	p := effectivePolicySettings{active: item.ProbeLimits.ActiveSeconds, backup: maxInt(item.ProbeLimits.ActiveSeconds*2, 120), mode: item.Mode}
	view := warmPlanningView(item)
	a, good := latestQuality(view, active)
	if !good {
		return
	}
	activeMedian := recentQualityMedian(now, view.Samples[active], p)
	if activeMedian == nil {
		return
	}
	if item.displayComparisons == nil {
		item.displayComparisons = map[string]displayLatencyComparison{}
	}
	for id := range item.CandidateLabels {
		b, ok := latestQuality(view, id)
		if !ok || !item.AvailabilityOK[id] || item.AvailabilityFailures[id] > 0 || !closeQualityPair(view, active, id) {
			continue
		}
		if isPairedPlanningMeasurement(qualityMeasurement(item.WarmPlanningMarker)) {
			activePhase, activeOK := cachedWarmPairEvidence(now, active, view)
			candidatePhase, candidateOK := cachedWarmPairEvidence(now, id, view)
			if !activeOK || !candidateOK || !compatibleQualityPhases(activePhase, candidatePhase) {
				continue
			}
		}
		median := recentQualityMedian(now, view.Samples[id], p)
		if median == nil {
			continue
		}
		item.displayComparisons[id] = displayLatencyComparison{
			Pair:    latencyComparison{Measurement: item.WarmPlanningMarker, Active: active, ActiveDelayMS: *activeMedian, CandidateDelayMS: *median, ActiveAt: a.At, CandidateAt: b.At},
			Context: context, ActiveFP: item.CandidateNodes[active].Fingerprint, CandidateFP: item.CandidateNodes[id].Fingerprint,
		}
	}
}
