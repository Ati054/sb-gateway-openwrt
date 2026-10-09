package healthcontract

import "encoding/json"

const (
	DefaultHistoryLimitKiB = 2048
	MinHistoryLimitKiB     = 256
	MaxHistoryLimitKiB     = 16384
	DefaultProbeBatch      = 10
	MaxProbeBatch          = 64
)

// Policy is the wire contract shared by the renderer and the controller.
// Draft-only routing fields and unknown legacy controls are not forwarded.
type Policy struct {
	HistoryLimitKiB        int                 `json:"history_limit_kib,omitempty"`
	LatencyMeasurement     string              `json:"latency_measurement,omitempty"`
	SwitchImprovementMS    int                 `json:"switch_improvement_ms"`
	ActiveCheckSeconds     int                 `json:"active_check_interval_seconds,omitempty"`
	ProbeBatchSize         int                 `json:"probe_batch_size"`
	ActiveLivenessSeconds  int                 `json:"active_liveness_interval_seconds,omitempty"`
	CandidateServiceIDs    []string            `json:"candidate_service_ids,omitempty"`
	CandidateServiceAccess map[string][]string `json:"candidate_service_access,omitempty"`
}

func (policy *Policy) UnmarshalJSON(body []byte) error {
	// Omitted tolerance uses the editor default; explicit zero is supported.
	type plain Policy
	value := plain{SwitchImprovementMS: 50}
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	*policy = Policy(value)
	return nil
}

type RoutingMonitor struct {
	ActiveLivenessSeconds *int `json:"active_liveness_interval_seconds,omitempty"`
	ActiveQualitySeconds  *int `json:"active_quality_interval_seconds,omitempty"`
	ProbeBatchSize        *int `json:"probe_batch_size,omitempty"`
}

func (monitor RoutingMonitor) ProbeBudget() int {
	if monitor.ProbeBatchSize == nil || *monitor.ProbeBatchSize <= 0 {
		return DefaultProbeBatch
	}
	return min(*monitor.ProbeBatchSize, MaxProbeBatch)
}
