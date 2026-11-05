package agentport

import "time"

// Successful planning history uses the quality interval, independently of how
// often a node serves as another candidate's physical reference. Raw evidence
// and its observation clock are processed before this admission decision.
func qualityHistoryAt(sample healthSample) float64 {
	return sample.At
}

func goodPlanningSample(sample healthSample) bool {
	return sample.OK && !sample.PrimaryQualityFailed && sample.DelayMS != nil
}

func qualityHistoryAllows(previous, next healthSample, p effectivePolicySettings) bool {
	if !goodPlanningSample(previous) || !goodPlanningSample(next) {
		return true // Failure and the first recovery are never hidden by cadence.
	}
	return qualityHistoryAt(next)-qualityHistoryAt(previous) >= float64(max(1, p.active))
}

func cadenceQualitySamples(now time.Time, samples []healthSample, p effectivePolicySettings) []healthSample {
	samples = recentQualitySamples(now, samples, p)
	kept := samples[:0]
	for _, sample := range samples {
		if len(kept) == 0 || qualityHistoryAllows(kept[len(kept)-1], sample, p) {
			kept = append(kept, sample)
		}
	}
	clear(samples[len(kept):])
	return kept
}

func appendQualityHistory(now time.Time, samples []healthSample, sample healthSample, p effectivePolicySettings) []healthSample {
	samples = cadenceQualitySamples(now, samples, p)
	if len(samples) == 0 || qualityHistoryAllows(samples[len(samples)-1], sample, p) {
		if sample.DelayMS != nil {
			delay := *sample.DelayMS
			sample.DelayMS = &delay
		}
		samples = append(samples, sample)
	}
	return recentQualitySamples(now, samples, p)
}

func rememberQualityObservation(item *policyHealthState, node string, sample healthSample) {
	if item.LatestQuality == nil {
		item.LatestQuality = make(map[string]healthSample)
	}
	if sample.DelayMS != nil {
		delay := *sample.DelayMS
		sample.DelayMS = &delay
	}
	item.LatestQuality[node] = sample
}

func ensureQualityHistoryCadence(item *policyHealthState, p effectivePolicySettings) {
	if p.mode != "best" {
		item.SampleCadenceSeconds = 0
		return
	}
	interval := max(1, p.active)
	if item.SampleCadenceSeconds == interval {
		return
	}
	// Old checkpoints and interval edits cannot provide a cadence-qualified
	// median. Preserve raw health, the working route and long-term statistics.
	for node, samples := range item.Samples {
		if len(samples) > 0 {
			if _, known := item.LatestQuality[node]; !known {
				rememberQualityObservation(item, node, samples[len(samples)-1])
			}
		}
	}
	item.Samples = make(map[string][]healthSample)
	item.SampleCadenceSeconds = interval
	clearOptimizationCandidate(item)
	item.OptimizationLastResult = nil
	item.LatencyComparisons, item.displayComparisons = nil, nil
}

func planningHistoryWindow(p effectivePolicySettings) float64 {
	return float64(maxInt(maxInt(p.backup*2, p.active*2), 120))
}

// Raw outcomes advance independently of successful history admission. The
// watermark survives an empty/expired planning ring and rejects late replay.
func freshQualityEvidence(item *policyHealthState, node string, at float64) bool {
	if item.qualityCountedAt == nil {
		item.qualityCountedAt = make(map[string]float64)
	}
	previous := latestQualityObservation(item, node)
	counted, known := item.qualityCountedAt[node]
	if at < 0 || known && at <= counted || previous.At > 0 && at <= previous.At {
		return false
	}
	item.qualityCountedAt[node] = at
	return true
}

func latestQualityObservation(item *policyHealthState, node string) healthSample {
	if item.planningSampleView {
		if value, exists := item.warmPlanningLatest[node]; exists {
			return healthSample{At: float64(value.ObservedAt.UnixNano()) / 1e9, OK: !value.Failed, DelayMS: value.DelayMS}
		}
	} else if sample, exists := item.LatestQuality[node]; exists {
		return sample
	}
	samples := item.Samples[node]
	if len(samples) == 0 {
		return healthSample{}
	}
	return samples[len(samples)-1]
}
