package agentport

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHistoryBudgetSharedAcrossPoliciesPreservesLiveEvidence(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	state := make(healthState)
	for p := 0; p < 4; p++ {
		item := newPolicyHealthState()
		item.CandidateLabels = make(map[string]string)
		item.AvailabilityOK = make(map[string]bool)
		item.Selected = "node-000"
		item.RuntimeSelected = item.Selected
		item.RuntimeConfirmed = true
		for n := 0; n < 250; n++ {
			node := fmt.Sprintf("node-%03d", n)
			item.CandidateLabels[node] = node
			delay := 500 + n
			item.Samples[node] = []healthSample{{At: float64(now.Unix()), OK: true, DelayMS: &delay}}
			item.AvailabilityOK[node] = true
			item.AvailabilityFailures[node] = 0
			item.Recoveries[node] = 3
			item.DailySamples[node] = make([]healthSample, 1440, 2000)
			for j := range item.DailySamples[node] {
				item.DailySamples[node][j] = item.Samples[node][0]
			}
			item.HistoryDays[node] = make(map[string]dayBucket)
			for day := 0; day < 7; day++ {
				values := make([]int, 32, 64)
				for j := range values {
					values[j] = delay
				}
				item.HistoryDays[node][now.AddDate(0, 0, -day).Format("2006-01-02")] = dayBucket{Samples: 1440, Successes: 1440, LatencySamples: values}
			}
		}
		state[fmt.Sprintf("route-%d", p)] = item
	}
	limit := 256 * 1024
	if !enforceHistoryBudget(state, now, limit) {
		t.Fatal("oversized history was retained")
	}
	if size := historyUsageBytes(state); size > limit {
		t.Fatalf("retained history exceeds shared budget: %d > %d", size, limit)
	}
	for _, item := range state {
		if !item.RuntimeConfirmed || item.Selected != "node-000" || item.RuntimeSelected != item.Selected || !item.AvailabilityOK[item.Selected] || item.Recoveries[item.Selected] != 3 || *item.Samples[item.Selected][0].DelayMS != 500 {
			t.Fatal("history eviction changed live routing evidence")
		}
		if item.HistoryDays[item.Selected]["2026-10-09"].Samples != 1440 {
			t.Fatal("active today's aggregate lost")
		}
		if !item.HistoryTruncated {
			t.Fatal("history eviction not visible")
		}
		for _, samples := range item.DailySamples {
			if cap(samples) > historyRecentSamples {
				t.Fatal("large raw backing array retained")
			}
		}
	}
	// The explicit retained-data charge must also cover its encoded RAM payload.
	history := make(map[string]any)
	for id, item := range state {
		history[id] = map[string]any{"days": item.HistoryDays, "recent": item.DailySamples}
	}
	encoded, err := json.Marshal(history)
	if err != nil || len(encoded) > limit {
		t.Fatalf("encoded history exceeds budget: %d %v", len(encoded), err)
	}
}

func TestHistoryBudgetMigratesLegacyCountersAndSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	item := newPolicyHealthState()
	item.CandidateLabels = make(map[string]string)
	item.Selected = "n"
	item.RuntimeSelected = "n"
	item.RuntimeConfirmed = true
	item.CandidateLabels["n"] = "Node"
	for i := 0; i < 1440; i++ {
		item.DailySamples["n"] = append(item.DailySamples["n"], healthSample{At: float64(now.Unix()) - float64(i), OK: i%2 == 0})
	}
	state := healthState{"route": item}
	enforceHistoryBudget(state, now, 256*1024)
	bucket := item.HistoryDays["n"]["2026-10-09"]
	if bucket.Samples != 1440 || bucket.Successes != 720 || bucket.Failures != 720 {
		t.Fatalf("migration lost full counts: %+v", bucket)
	}
	if len(item.DailySamples["n"]) != 32 || cap(item.DailySamples["n"]) != 32 {
		t.Fatal("legacy raw buffer was not released")
	}
	path := filepath.Join(t.TempDir(), "selector-health.json")
	controller := healthController{historyLimitBytes: 256 * 1024}
	if err := controller.publishState(path, state, now); err != nil {
		t.Fatal(err)
	}
	var loaded healthState
	if err := readJSON(path, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded["route"].Selected != "n" || !reflect.DeepEqual(loaded["route"].HistoryDays, item.HistoryDays) {
		t.Fatal("service restart lost history or route")
	}
}

func TestHistoryBudgetRemovesOldestDaysFirstAndHonorsReduction(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	item := newPolicyHealthState()
	item.Selected = "active"
	item.RuntimeSelected = "active"
	for _, node := range []string{"active", "reserve"} {
		item.HistoryDays[node] = map[string]dayBucket{"2026-10-08": {Samples: 10}, "2026-10-09": {Samples: 20}}
	}
	state := healthState{"route": item}
	before := historyUsageBytes(state)
	enforceHistoryBudget(state, now, before-historyDayCharge("2026-10-08", dayBucket{}))
	if _, exists := item.HistoryDays["reserve"]["2026-10-08"]; exists {
		t.Fatal("old reserve day not evicted first")
	}
	if item.HistoryDays["active"]["2026-10-09"].Samples != 20 {
		t.Fatal("new active day evicted")
	}
	if historyUsageBytes(state) > before-historyDayCharge("2026-10-08", dayBucket{}) {
		t.Fatal("reduced budget not applied")
	}
}

func TestRecentHistoryRingKeepsAllDayCountersWithoutGrowing(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	item := newPolicyHealthState()
	for i := 0; i < 10000; i++ {
		updateHistories(item, []string{"n"}, map[string]probeEvidence{"n": {OK: i%2 == 0}}, now.Add(time.Duration(i)*time.Second))
	}
	if len(item.DailySamples["n"]) != 32 || cap(item.DailySamples["n"]) != 32 {
		t.Fatal("raw sample ring grew beyond 32")
	}
	stats := summarizeDays(item.HistoryDays["n"], 1, now.Add(10000*time.Second))
	if stats.Samples != 10000 || stats.Successes != 5000 || stats.Failures != 5000 {
		t.Fatalf("ring trimming altered counters: %+v", stats)
	}
}

func TestHistoryLimitUsesDefaultAndSmallestValidatedSharedContract(t *testing.T) {
	if got := historyLimitBytes(healthPool{}); got != 2048*1024 {
		t.Fatal(got)
	}
	pool := healthPool{HealthPolicies: map[string]healthPolicyContract{"a": {Policy: healthPolicy{HistoryLimitKiB: 16384}}, "b": {Policy: healthPolicy{HistoryLimitKiB: 256}}}}
	if got := historyLimitBytes(pool); got != 256*1024 {
		t.Fatal(got)
	}
	pool.HealthPolicies["b"] = healthPolicyContract{Policy: healthPolicy{HistoryLimitKiB: -1}}
	if got := historyLimitBytes(pool); got != 256*1024 {
		t.Fatal("invalid contract bypassed floor", got)
	}
}
