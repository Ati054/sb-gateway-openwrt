package agentport

import (
	"github.com/Ati054/sb-gateway-openwrt/internal/healthcontract"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type RuntimeSnapshot = healthcontract.RuntimeSnapshot
type RuntimePolicy = healthcontract.RuntimePolicy

type MonitorSnapshot struct {
	RuntimeSnapshot
	Health []MonitorPolicy `json:"health"`
}

type MonitorPolicy struct {
	HistoryTruncated bool          `json:"history_truncated,omitempty"`
	HistoryBudgetKiB int           `json:"history_budget_kib,omitempty"`
	HistoryUsageKiB  int           `json:"history_usage_kib,omitempty"`
	ID               string        `json:"id"`
	Mode             string        `json:"mode"`
	Selected         string        `json:"selected"`
	HealthyReserves  int           `json:"healthy_reserves"`
	CandidateCount   int           `json:"candidate_count"`
	CheckedAt        string        `json:"checked_at"`
	UnderlayFailure  string        `json:"underlay_failure,omitempty"`
	ProbeLimits      probeLimits   `json:"probe_limits"`
	Nodes            []MonitorNode `json:"nodes"`
}

type MonitorNode struct {
	ID              string                  `json:"id"`
	Label           string                  `json:"label"`
	Order           *int                    `json:"candidate_order,omitempty"`
	Selected        bool                    `json:"selected"`
	Available       bool                    `json:"available"`
	LastProbeAt     float64                 `json:"last_probe_at,omitempty"`
	FailureClass    string                  `json:"failure_class,omitempty"`
	CurrentMedianMS *int                    `json:"current_median_ms"`
	LastGoodAt      float64                 `json:"last_good_at,omitempty"`
	LatestDelayMS   *int                    `json:"latest_delay_ms"`
	LatestQualityAt float64                 `json:"latest_quality_at,omitempty"`
	PeriodStats     map[string]MonitorStats `json:"period_stats"`
}

type MonitorStats struct {
	Samples      int      `json:"samples"`
	Availability *float64 `json:"availability_percent,omitempty"`
	MedianMS     *int     `json:"median_ms,omitempty"`
	Loss         *float64 `json:"https_error_percent,omitempty"`
}

func runtimeSnapshot(state healthState, now time.Time) RuntimeSnapshot {
	result := RuntimeSnapshot{UpdatedAt: now.UTC(), Policies: make(map[string]RuntimePolicy, len(state))}
	for id, item := range state {
		if item == nil {
			continue
		}
		result.Policies[id] = RuntimePolicy{Selected: item.Selected, RuntimeSelected: item.RuntimeSelected,
			RuntimeConfirmed: item.RuntimeConfirmed, WANFallbackActive: item.WANFallbackActive,
			RuntimeError: item.RuntimeError, LastSwitchReason: item.LastSwitchReason, CheckedAt: item.CheckedAt}
	}
	return result
}

// Reuse aggregates already maintained by the health worker. Raw samples and
// daily reservoirs never enter the monitoring files or the browser response.
func monitorSnapshot(state healthState, runtime RuntimeSnapshot) MonitorSnapshot {
	result := MonitorSnapshot{RuntimeSnapshot: runtime, Health: make([]MonitorPolicy, 0, len(state))}
	for id, item := range state {
		if item == nil {
			continue
		}
		entry := MonitorPolicy{ID: strings.TrimPrefix(id, "resproxy-"), Mode: item.Mode, Selected: item.RuntimeSelected,
			HealthyReserves: item.HealthyReserves, CandidateCount: item.CandidateCount, CheckedAt: item.CheckedAt,
			UnderlayFailure: item.UnderlayFailure, HistoryTruncated: item.HistoryTruncated,
			HistoryBudgetKiB: item.HistoryBudgetKiB, HistoryUsageKiB: item.HistoryUsageKiB,
			ProbeLimits: item.ProbeLimits, Nodes: make([]MonitorNode, 0, len(item.CandidateLabels))}
		order := map[string]int{}
		for rank, node := range strings.Split(item.CandidateSignature, "\n") {
			order[node] = rank
		}
		for id, label := range item.CandidateLabels {
			node := MonitorNode{ID: id, Label: label, Selected: item.RuntimeConfirmed && id == item.RuntimeSelected,
				Available: item.AvailabilityOK[id], LastProbeAt: item.LastProbeAt[id], FailureClass: item.FailureClass[id],
				PeriodStats: map[string]MonitorStats{}}
			if rank, ok := order[id]; ok {
				node.Order = &rank
			}
			if at := item.LastGoodAt[id]; at > 0 {
				if delay := item.DelayMS[id]; delay != nil && *delay >= 0 {
					node.CurrentMedianMS, node.LastGoodAt = delay, at
				}
			}
			if samples := item.Samples[id]; len(samples) > 0 {
				latest := samples[len(samples)-1]
				if latest.OK && latest.DelayMS != nil && latest.At > 0 {
					node.LatestDelayMS, node.LatestQualityAt = latest.DelayMS, latest.At
				}
			}
			for _, period := range []string{"today", "7d"} {
				stats := item.PeriodStats[period][id]
				if period == "today" {
					stats = item.DailyStats[id]
				}
				node.PeriodStats[period] = MonitorStats{Samples: stats.Samples, Availability: stats.AvailabilityPercent, MedianMS: stats.MedianMS, Loss: stats.LossPercent}
			}
			entry.Nodes = append(entry.Nodes, node)
		}
		sort.Slice(entry.Nodes, func(i, j int) bool { return entry.Nodes[i].ID < entry.Nodes[j].ID })
		result.Health = append(result.Health, entry)
	}
	sort.Slice(result.Health, func(i, j int) bool { return result.Health[i].ID < result.Health[j].ID })
	return result
}

func (controller *healthController) publishMonitor(path string, state healthState, now time.Time) error {
	day := now.UTC().Format("2006-01-02")
	if controller.monitorDay != day {
		for _, item := range state {
			if item != nil {
				refreshHistoryStats(item, now)
			}
		}
	}
	runtime := runtimeSnapshot(state, time.Now())
	if err := writeRAMJSONAtomic(filepath.Join(filepath.Dir(path), "selector-runtime.json"), runtime); err != nil {
		return err
	}
	controller.monitorRuntimeAt = time.Now()
	if err := writeRAMJSONAtomic(filepath.Join(filepath.Dir(path), "monitor.json"), monitorSnapshot(state, runtime)); err != nil {
		return err
	}
	controller.monitorDay = day
	return nil
}

// Successful fast checks renew only this tiny lease. They do not reserialize
// measurement history or rebuild the browser's aggregate view.
func publishRuntimeSnapshot(path string, state healthState) error {
	return writeRAMJSONAtomic(filepath.Join(filepath.Dir(path), "selector-runtime.json"), runtimeSnapshot(state, time.Now()))
}

func (controller *healthController) renewRuntime(path string, state healthState, now time.Time) error {
	if controller.monitorDay != now.UTC().Format("2006-01-02") {
		return controller.publishMonitor(path, state, now)
	}
	// Busy quality workers service the fast lane every second. A five-second
	// heartbeat is sufficient for the twenty-second DNS lease; changed state is
	// always published immediately by publishMonitor, independently of this gate.
	if !controller.monitorRuntimeAt.IsZero() && time.Since(controller.monitorRuntimeAt) < 5*time.Second {
		return nil
	}
	if err := publishRuntimeSnapshot(path, state); err != nil {
		return err
	}
	controller.monitorRuntimeAt = time.Now()
	return nil
}
