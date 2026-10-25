package agentport

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMonitorPublishesCompactViewsAndRevokesStartupWAN(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "selector-health.json")
	now := time.Now()
	item := newPolicyHealthState()
	item.Selected, item.RuntimeSelected, item.RuntimeConfirmed, item.WANFallbackActive = "block", "block", true, true
	item.CandidateLabels = map[string]string{"a": "<img src=x>", "b": "B"}
	item.CandidateSignature = "b\na"
	delay := 300
	item.LastGoodAt["a"] = float64(now.Unix())
	item.DelayMS = map[string]*int{"a": &delay}
	item.Samples["a"] = []healthSample{{At: float64(now.Unix()), OK: true, DelayMS: &delay}}
	item.HistoryDays["a"] = map[string]dayBucket{now.UTC().Format("2006-01-02"): {Samples: 17, Successes: 17, LatencySamples: []int{100, 200}}}
	state := healthState{"resproxy-main": item}
	controller := &healthController{}
	// A long probe round must not backdate the runtime publication lease.
	if err := controller.publishState(path, state, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	runtimeBody, err := os.ReadFile(filepath.Join(root, "selector-runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var runtime RuntimeSnapshot
	if err := json.Unmarshal(runtimeBody, &runtime); err != nil {
		t.Fatal(err)
	}
	if !runtime.Policies["resproxy-main"].WANFallbackActive || time.Since(runtime.UpdatedAt) > time.Second {
		t.Fatal("lost confirmed WAN or backdated publication", runtime)
	}
	if _, err := os.Stat("/proc/uptime"); err == nil && runtime.UpdatedUptimeSeconds <= 0 {
		t.Fatal("runtime lease has no monotonic publication timestamp")
	}
	body, err := os.ReadFile(filepath.Join(root, "monitor.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"history_days", "daily_samples", "latency_samples", "credentials", "candidate_nodes"} {
		if bytes.Contains(body, []byte(private)) || bytes.Contains(runtimeBody, []byte(private)) {
			t.Fatalf("history/internal field escaped: %s", private)
		}
	}
	var view MonitorSnapshot
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Health) != 1 || len(view.Health[0].Nodes) != 2 {
		t.Fatal(view)
	}
	// Projection uses maintained aggregates and preserves configured node order.
	// Use today's date for the next publication to also exercise UTC rollover.
	if err := controller.publishState(path, state, now); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(filepath.Join(root, "monitor.json"))
	json.Unmarshal(body, &view)
	node := view.Health[0].Nodes[0]
	if node.Order == nil || *node.Order != 1 || node.PeriodStats["today"].Samples != 17 || node.CurrentMedianMS == nil || *node.CurrentMedianMS != 300 || node.Label != "<img src=x>" {
		t.Fatal(node)
	}
	before := len(item.HistoryDays["a"])
	if err := InvalidateRuntimeHealth(root); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(filepath.Join(root, "selector-runtime.json"))
	json.Unmarshal(body, &runtime)
	policy := runtime.Policies["resproxy-main"]
	if policy.RuntimeConfirmed || policy.WANFallbackActive || policy.RuntimeSelected != "" {
		t.Fatal("startup reused old WAN proof", policy)
	}
	retained := healthState{}
	if err := readJSON(path, &retained); err != nil {
		t.Fatal(err)
	}
	if len(retained["resproxy-main"].HistoryDays["a"]) != before {
		t.Fatal("projection removed retained measurements")
	}
}

func TestMonitorEmptyStartupRemovesOldView(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "monitor.json"), []byte(`{"health":[{"id":"old"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InvalidateRuntimeHealth(root); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "monitor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var view MonitorSnapshot
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Health) != 0 || len(view.Policies) != 0 {
		t.Fatal("old monitor survived missing history")
	}
}

func TestFastCheckRenewsOnlyCompactRuntime(t *testing.T) {
	root := t.TempDir()
	pool := healthFixture(false)
	contract := pool.HealthPolicies["europe"]
	contract.Policy.ActiveCheckSeconds = 60
	pool.HealthPolicies["europe"] = contract
	runtime := &fakeSelectorRuntime{pool: pool, current: map[string]string{"europe": "de"}, probes: map[string]probeEvidence{"de": successfulEvidence(100), "nl": successfulEvidence(200)}}
	controller := &healthController{opts: Options{StateRoot: root, HealthInterval: time.Minute}, runtime: runtime}
	now := time.Now()
	if err := controller.Tick(now); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(root, "selector-health.json")
	before, err := os.ReadFile(history)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(history)
	if err != nil {
		t.Fatal(err)
	}
	leaseBefore, _ := os.ReadFile(filepath.Join(root, "selector-runtime.json"))
	// Fast checks retain the full history file and renew just the runtime lease.
	controller.monitorRuntimeAt = time.Now().Add(-6 * time.Second)
	if err := controller.Tick(now.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(history)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(history)
	if err != nil {
		t.Fatal(err)
	}
	leaseAfter, err := os.ReadFile(filepath.Join(root, "selector-runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("unchanged fast check rewrote full history")
	}
	if bytes.Equal(leaseBefore, leaseAfter) {
		t.Fatal("fast check did not renew compact lease")
	}
	if len(runtime.availabilityCalls) == 0 {
		t.Fatal("lease renewed without checking the active path")
	}
	if err := controller.renewRuntime(history, controller.state, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	gated, err := os.ReadFile(filepath.Join(root, "selector-runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leaseAfter, gated) {
		t.Fatal("rapid callbacks bypassed the heartbeat rate limit")
	}
}

func TestMonitorUTCPeriodRolloverWithoutNewMeasurement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "selector-health.json")
	now := time.Date(2026, 10, 16, 0, 0, 1, 0, time.UTC)
	previous := now.Add(-2 * time.Second)
	item := newPolicyHealthState()
	item.CandidateLabels = map[string]string{"node": "Node"}
	item.HistoryDays["node"] = map[string]dayBucket{previous.Format("2006-01-02"): {Samples: 17, Successes: 17, LatencySamples: []int{100}}}
	state := healthState{"resproxy-main": item}
	controller := &healthController{}
	if err := controller.publishState(path, state, previous); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.renewRuntime(path, state, now); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "monitor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot MonitorSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	stats := snapshot.Health[0].Nodes[0].PeriodStats
	if stats["today"].Samples != 0 || stats["7d"].Samples != 17 {
		t.Fatal("periods retained yesterday's date", stats)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("calendar-only projection rewrote measurement history")
	}
}
