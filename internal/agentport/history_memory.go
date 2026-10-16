package agentport

import (
	"sort"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/healthcontract"
)

const historyRecentSamples = 32

// The budget bounds retained history, not process RSS. Charges include generous
// allowance for Go maps, strings, slice capacity and individually allocated
// latency values. Current route evidence and inventory are accounted separately.
func historyNodeCharge(node string) int { return 256 + 8*len(node) }
func historyDayCharge(day string, bucket dayBucket) int {
	return 512 + 8*len(day) + 8*cap(bucket.LatencySamples)
}
func historySampleCharge(node string, samples []healthSample) int {
	return historyNodeCharge(node) + 96*cap(samples)
}

func historyUsageBytes(state healthState) int {
	size := 0
	for id, item := range state {
		if item == nil {
			continue
		}
		size += 256 + 8*len(id)
		for node, samples := range item.DailySamples {
			size += historySampleCharge(node, samples)
		}
		for node, days := range item.HistoryDays {
			size += historyNodeCharge(node)
			for day, bucket := range days {
				size += historyDayCharge(day, bucket)
			}
		}
	}
	return size
}

func historyLimitBytes(pool healthPool) int {
	limit := healthcontract.DefaultHistoryLimitKiB
	configured := false
	for _, contract := range pool.HealthPolicies {
		value := contract.Policy.HistoryLimitKiB
		if value == 0 {
			value = healthcontract.DefaultHistoryLimitKiB
		}
		value = min(max(value, healthcontract.MinHistoryLimitKiB), healthcontract.MaxHistoryLimitKiB)
		if !configured || value < limit {
			limit, configured = value, true
		}
	}
	return limit * 1024
}

func (controller *healthController) publishState(path string, state healthState, now time.Time) error {
	enforceHistoryBudget(state, now, controller.historyLimitBytes)
	if err := writeJSONAtomic(path, state); err != nil {
		return err
	}
	return controller.publishMonitor(path, state, now)
}

// Compact duplicate raw samples first; daily aggregates keep the full counts.
// If the shared budget is still exceeded, evict the oldest day across all route
// lists. Active-node records are the last evicted for an equal date. The current
// Samples/availability/selection maps are never touched by history eviction.
func enforceHistoryBudget(state healthState, now time.Time, limit int) (changed bool) {
	if limit <= 0 {
		limit = healthcontract.DefaultHistoryLimitKiB * 1024
	}
	oldest := now.UTC().AddDate(0, 0, -(historyRetentionDays - 1)).Format("2006-01-02")
	today := now.UTC().Format("2006-01-02")
	defer func() {
		usedKiB := (historyUsageBytes(state) + 1023) / 1024
		for _, item := range state {
			if item == nil {
				continue
			}
			if item.HistoryBudgetKiB != limit/1024 || item.HistoryUsageKiB != usedKiB {
				changed = true
			}
			item.HistoryBudgetKiB, item.HistoryUsageKiB = limit/1024, usedKiB
		}
	}()
	for _, item := range state {
		if item == nil {
			continue
		}
		for node, samples := range item.DailySamples {
			// Upgrade legacy raw history before releasing its large backing array.
			if len(samples) > 0 && item.HistoryDays[node] == nil {
				if item.HistoryDays == nil {
					item.HistoryDays = make(map[string]map[string]dayBucket)
				}
				days := make(map[string]dayBucket)
				for _, sample := range samples {
					day := time.Unix(int64(sample.At), 0).UTC().Format("2006-01-02")
					if day >= oldest && day <= today {
						appendHistoryDay(days, sample)
					}
				}
				item.HistoryDays[node] = days
				changed = true
			}
			keep := min(len(samples), historyRecentSamples)
			if len(samples) != keep || cap(samples) > historyRecentSamples {
				item.DailySamples[node] = append(make([]healthSample, 0, keep), samples[len(samples)-keep:]...)
				changed = true
			}
		}
		for node, days := range item.HistoryDays {
			for day, bucket := range days {
				if day < oldest || day > today {
					delete(days, day)
					changed = true
					continue
				}
				keep := min(len(bucket.LatencySamples), historyReservoir)
				if len(bucket.LatencySamples) != keep || cap(bucket.LatencySamples) > historyReservoir {
					bucket.LatencySamples = append(make([]int, 0, keep), bucket.LatencySamples[:keep]...)
					days[day] = bucket
					changed = true
				}
			}
			if len(days) == 0 {
				delete(item.HistoryDays, node)
				changed = true
			}
		}
	}
	usage := historyUsageBytes(state)
	if usage <= limit {
		return changed
	}
	type record struct {
		policy, node, day string
		active            bool
		at                float64
	}
	var samples, days []record
	for policy, item := range state {
		if item == nil {
			continue
		}
		for node, values := range item.DailySamples {
			at := float64(0)
			if len(values) > 0 {
				at = values[len(values)-1].At
			}
			samples = append(samples, record{policy: policy, node: node, active: node == item.Selected || node == item.RuntimeSelected, at: at})
		}
		for node, buckets := range item.HistoryDays {
			for day := range buckets {
				days = append(days, record{policy: policy, node: node, day: day, active: node == item.Selected || node == item.RuntimeSelected})
			}
		}
	}
	less := func(a, b record) bool {
		if a.day != b.day {
			return a.day < b.day
		}
		if a.at != b.at {
			return a.at < b.at
		}
		if a.active != b.active {
			return !a.active
		}
		if a.policy != b.policy {
			return a.policy < b.policy
		}
		return a.node < b.node
	}
	sort.Slice(samples, func(i, j int) bool { return less(samples[i], samples[j]) })
	sort.Slice(days, func(i, j int) bool { return less(days[i], days[j]) })
	touched := make(map[string]bool)
	for _, record := range samples {
		if usage <= limit {
			break
		}
		item := state[record.policy]
		usage -= historySampleCharge(record.node, item.DailySamples[record.node])
		delete(item.DailySamples, record.node)
		touched[record.policy] = true
	}
	for _, record := range days {
		if usage <= limit {
			break
		}
		item := state[record.policy]
		buckets := item.HistoryDays[record.node]
		usage -= historyDayCharge(record.day, buckets[record.day])
		delete(buckets, record.day)
		if len(buckets) == 0 {
			usage -= historyNodeCharge(record.node)
			delete(item.HistoryDays, record.node)
		}
		item.HistoryTruncated = true
		touched[record.policy] = true
	}
	for policy := range touched {
		item := state[policy]
		// Release oversized map backing storage after evicting entries. The old
		// allocation becomes collectable; instantaneous RSS also depends on Go GC.
		compactSamples := make(map[string][]healthSample, len(item.DailySamples))
		for node, value := range item.DailySamples {
			compactSamples[node] = value
		}
		item.DailySamples = compactSamples
		compactDays := make(map[string]map[string]dayBucket, len(item.HistoryDays))
		for node, values := range item.HistoryDays {
			copyDays := make(map[string]dayBucket, len(values))
			for day, value := range values {
				copyDays[day] = value
			}
			compactDays[node] = copyDays
		}
		item.HistoryDays = compactDays
		refreshHistoryStats(item, now)
	}
	return true
}

func refreshHistoryStats(item *policyHealthState, now time.Time) {
	item.DailyStats = make(map[string]healthStats, len(item.CandidateLabels))
	period7 := make(map[string]healthStats, len(item.CandidateLabels))
	for node := range item.CandidateLabels {
		item.DailyStats[node] = summarizeDays(item.HistoryDays[node], 1, now)
		period7[node] = summarizeDays(item.HistoryDays[node], historyRetentionDays, now)
	}
	item.PeriodStats = map[string]map[string]healthStats{"24h": item.DailyStats, "7d": period7, "30d": period7}
}
