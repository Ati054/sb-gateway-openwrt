package agentport

import "time"

// HistoryDay is the bounded, dated snapshot shared with the read-only UI API.
type HistoryDay = dayBucket
type HistoryStats = healthStats

// SummarizeHistoryDays reads history independently of a running probe batch.
func SummarizeHistoryDays(days map[string]HistoryDay, count int, now time.Time) HistoryStats {
	return summarizeDays(days, count, now)
}
