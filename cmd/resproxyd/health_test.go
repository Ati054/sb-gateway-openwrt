package main

import (
	"encoding/json"
	"github.com/Ati054/sb-gateway-openwrt/internal/daemon"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthPublishesSuccessfulMeasurementTimesAfterFailedAttempt(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "health-state"), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"resproxy-main":{"runtime_selected":"node","runtime_confirmed":true,"candidate_signature":"changed\nnode\nlegacy","candidate_labels":{"node":"Node","legacy":"Legacy","changed":"Changed"},"delay_ms":{"node":25,"legacy":50},"last_good_at":{"node":1000},"last_probe_at":{"node":2000},"speed_samples_bps":{"node":[100,200],"legacy":[500]},"last_speed_success_at":{"node":900},"last_speed_measurement":{"changed":{"at":800,"bps":300}}}}`)
	var fixture map[string]map[string]any
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	fixture["resproxy-main"]["history_days"] = map[string]any{"changed": map[string]any{time.Now().UTC().Format("2006-01-02"): map[string]any{"samples": 17, "successes": 17, "failures": 0, "latency_samples": []int{100, 200}}}}
	fixture["resproxy-main"]["period_stats"] = map[string]any{"today": map[string]any{"changed": map[string]any{"samples": 1}}}
	body, _ = json.Marshal(fixture)
	if err := os.WriteFile(filepath.Join(root, "health-state", "selector-health.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w
	err = printHealth(daemon.Paths{RunRoot: root})
	os.Stdout = previous
	w.Close()
	defer r.Close()
	if err != nil {
		t.Fatal(err)
	}
	var policies []policyHealth
	if err := json.NewDecoder(r).Decode(&policies); err != nil {
		t.Fatal(err)
	}
	for _, n := range policies[0].Nodes {
		if n.ID == "node" {
			if n.Order == nil || *n.Order != 1 {
				t.Fatal("configured order missing", n.Order)
			}
			if n.CurrentMedianMS == nil || *n.CurrentMedianMS != 25 || n.LastGoodAt != 1000 || n.LastProbeAt != 2000 {
				t.Fatalf("failed attempt changed successful metrics: %+v", n)
			}
		}
		if n.ID == "changed" && n.Available {
			t.Fatal("changed endpoint lost dated last measurement or was reported available")
		}
		if n.ID == "changed" && n.PeriodStats["today"].Samples != 17 {
			t.Fatalf("UI reads stale cached summary: %+v", n)
		}
		if n.ID == "legacy" && n.CurrentMedianMS != nil {
			t.Fatal("undated legacy values presented as current")
		}
	}
}

func TestFreshLatencyDisplayAfterActiveSwitchAndExpiry(t *testing.T) {
	now := time.Unix(2000, 0)
	delay := func(v int) *int { return &v }
	nodes := []nodeHealth{
		{ID: "new-active", Selected: true, Available: true, LatestDelayMS: delay(450), LatestQualityAt: 1990},
		{ID: "candidate", Available: true, LatestDelayMS: delay(300), LatestQualityAt: 1980},
		{ID: "stale", Available: true, LatestDelayMS: delay(100), LatestQualityAt: 1800},
		{ID: "failed", Available: false, LatestDelayMS: delay(200), LatestQualityAt: 1990},
		{ID: "future", Available: true, LatestDelayMS: delay(100), LatestQualityAt: 2010},
	}
	setFreshLatencyDeltas(nodes, now, 60, 180)
	if nodes[0].LatencyDelta == nil || *nodes[0].LatencyDelta != 0 || nodes[1].LatencyDelta == nil || *nodes[1].LatencyDelta < 33.3 || *nodes[1].LatencyDelta > 33.4 {
		t.Fatal(nodes)
	}
	for _, n := range nodes[2:] {
		if n.LatencyDelta != nil || n.DeltaReason == "" {
			t.Fatal("invalid sample compared", n)
		}
	}
	nodes[0].Selected = false
	nodes[1].Selected = true
	setFreshLatencyDeltas(nodes, now, 60, 180)
	if nodes[0].LatencyDelta == nil || *nodes[0].LatencyDelta != -50 || *nodes[1].LatencyDelta != 0 {
		t.Fatal("comparison retained old active", nodes)
	}
	setFreshLatencyDeltas(nodes, now.Add(121*time.Second), 60, 180)
	for _, n := range nodes {
		if n.LatencyDelta != nil || n.DeltaReason != "active-measurement-missing-or-stale" {
			t.Fatal("expired active compared", n)
		}
	}
	nodes[1].Selected = false
	setFreshLatencyDeltas(nodes, now, 60, 180)
	for _, n := range nodes {
		if n.LatencyDelta != nil {
			t.Fatal("unconfirmed active compared", n)
		}
	}
}

func TestHealthUsesPreparedViewWithoutParsingRetainedHistory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "health-state")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	// An unreadable legacy payload proves the normal monitoring path bypasses it.
	if err := os.WriteFile(filepath.Join(directory, "selector-health.json"), []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Unix()
	view := map[string]any{"health": []any{map[string]any{"id": "main", "probe_limits": map[string]int{"active_seconds": 60, "backup_seconds": 180}, "nodes": []any{
		map[string]any{"id": "a", "selected": true, "available": true, "latest_delay_ms": 400, "latest_quality_at": at},
		map[string]any{"id": "b", "available": true, "latest_delay_ms": 300, "latest_quality_at": at},
	}}}}
	body, _ := json.Marshal(view)
	if err := os.WriteFile(filepath.Join(directory, "monitor.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w
	err = printHealth(daemon.Paths{RunRoot: root})
	os.Stdout = previous
	w.Close()
	defer r.Close()
	if err != nil {
		t.Fatal(err)
	}
	var policies []policyHealth
	if err := json.NewDecoder(r).Decode(&policies); err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].Nodes[1].LatencyDelta == nil || *policies[0].Nodes[1].LatencyDelta != 25 {
		t.Fatal(policies)
	}
}
