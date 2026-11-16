package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// One small incident record survives RAM-log loss. It deliberately contains no
// URLs, node credentials, user labels, raw errors or network configuration.
type ServiceFailure struct {
	At             string `json:"at"`
	Stage          string `json:"stage"`
	CaptureRemoved bool   `json:"capture_removed"`
	PolicyRetained bool   `json:"source_policy_retained"`
	DNSRestored    bool   `json:"dns_restored"`
	BridgeRestored bool   `json:"bridge_netfilter_restored"`
	RecoveredAt    string `json:"recovered_at,omitempty"`
}

func failureRecordPath(paths Paths) string { return filepath.Join(paths.DataRoot, "last-failure.json") }

func LastServiceFailure(paths Paths) *ServiceFailure {
	file, err := os.Open(failureRecordPath(paths))
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil
	}
	var record ServiceFailure
	if json.NewDecoder(file).Decode(&record) != nil {
		return nil
	}
	return &record
}

func recordServiceFailure(paths Paths, stage string, captureRemoved, dnsRestored, bridgeRestored bool) error {
	record := ServiceFailure{At: time.Now().UTC().Format(time.RFC3339Nano), Stage: stage, PolicyRetained: CrashFallbackActive(paths)}
	if previous := LastServiceFailure(paths); previous != nil && previous.RecoveredAt == "" {
		if previous.CaptureRemoved == captureRemoved && previous.DNSRestored == dnsRestored && previous.BridgeRestored == bridgeRestored && previous.PolicyRetained == record.PolicyRetained {
			return nil // A respawn loop must not write flash on every attempt.
		}
		retained := record.PolicyRetained
		record = *previous // Preserve the original incident time and stage.
		record.PolicyRetained = retained
	}
	record.CaptureRemoved, record.DNSRestored, record.BridgeRestored = captureRemoved, dnsRestored, bridgeRestored
	if err := os.MkdirAll(paths.DataRoot, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeDurableDNSMarker(failureRecordPath(paths), data)
}

func markServiceRecovered(paths Paths) error {
	record := LastServiceFailure(paths)
	if record == nil || record.RecoveredAt != "" {
		return nil
	}
	record.RecoveredAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeDurableDNSMarker(failureRecordPath(paths), data)
}
