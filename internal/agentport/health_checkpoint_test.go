package agentport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var checkpointCPUNanos func() int64

func checkpointFixture(t testing.TB) (*healthController, healthState, string, *time.Time) {
	t.Helper()
	now := time.Now()
	item := newPolicyHealthState()
	item.Selected, item.RuntimeSelected, item.RuntimeConfirmed = "active", "active", true
	item.AvailabilityOK = map[string]bool{"active": true}
	item.QualityOK = map[string]bool{"active": true}
	item.CandidateLabels = map[string]string{"active": "active"}
	item.Mode = "best"
	state := healthState{"route": item}
	root := t.TempDir()
	c := &healthController{opts: Options{StateRoot: root}, state: state, checkpointNow: func() time.Time { return now }}
	return c, state, filepath.Join(root, "selector-health.json"), &now
}

func TestCheckpointCoalescesHistoryButPublishesLiveView(t *testing.T) {
	c, state, path, now := checkpointFixture(t)
	if err := c.publishRoutineState(path, state, *now); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	state["route"].CheckedAt = "new-result"
	state["route"].AvailabilityFailures["active"] = 0
	*now = now.Add(time.Second)
	if err := c.publishRoutineState(path, state, *now); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) || !c.checkpointPending {
		t.Fatal("volatile result forced a full checkpoint")
	}
	var view MonitorSnapshot
	if err := readJSON(filepath.Join(filepath.Dir(path), "monitor.json"), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Health) != 1 || view.Health[0].CheckedAt != "new-result" {
		t.Fatal("coalescing hid live monitoring")
	}
	*now = now.Add(healthCheckpointInterval)
	if err := c.publishRoutineState(path, state, *now); err != nil {
		t.Fatal(err)
	}
	second, _ = os.ReadFile(path)
	if string(first) == string(second) || c.checkpointPending {
		t.Fatal("latest state did not flush at bounded interval")
	}
}

func TestCheckpointCriticalChangesAndImmediateWriteAreNotDeferred(t *testing.T) {
	for _, change := range []string{"selected", "failure", "recovered", "membership", "runtime-error", "working", "wan"} {
		t.Run(change, func(t *testing.T) {
			c, state, path, now := checkpointFixture(t)
			item := state["route"]
			if change == "recovered" {
				item.AvailabilityFailures["active"] = 1
			}
			if err := c.publishState(path, state, *now); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			switch change {
			case "selected":
				item.Selected, item.RuntimeSelected = "reserve", "reserve"
			case "failure":
				item.AvailabilityFailures["active"] = 1
			case "recovered":
				item.AvailabilityFailures["active"] = 0
			case "membership":
				item.CandidateSignature = "active\nreserve"
			case "runtime-error":
				item.RuntimeConfirmed = false
				item.RuntimeError = "API unavailable"
			case "working":
				item.LastWorkingSelection = &workingSelection{Selected: "active", Mode: "best"}
			case "wan":
				item.WANFallbackActive = true
			}
			*now = now.Add(time.Second)
			if err := c.publishRoutineState(path, state, *now); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(before) == string(after) || c.checkpointPending {
				t.Fatal("critical change was deferred")
			}
			stamp := c.checkpointAt
			if err := c.publishRoutineState(path, state, *now); err != nil {
				t.Fatal(err)
			}
			if c.checkpointAt != stamp {
				t.Fatal("forced write was repeated by next routine publication")
			}
		})
	}
}

func TestCheckpointMaintenancePreservesDeadlineAndFlushesLatest(t *testing.T) {
	c, state, path, now := checkpointFixture(t)
	if err := c.publishState(path, state, *now); err != nil {
		t.Fatal(err)
	}
	state["route"].CheckedAt = "pending-latest"
	c.checkpointAt = time.Now().Add(-healthCheckpointInterval + 40*time.Millisecond)
	c.checkpointPending = true
	c.checkpointNow = nil
	started := time.Now()
	if !c.waitWithHealthCheckpoint(t.Context(), 150*time.Millisecond, nil, 10*time.Millisecond, nil, nil) {
		t.Fatal("maintenance canceled health wait")
	}
	if elapsed := time.Since(started); elapsed < 140*time.Millisecond || elapsed > time.Second {
		t.Fatalf("maintenance changed deadline: %s", elapsed)
	}
	var saved healthState
	if err := readJSON(path, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["route"].CheckedAt != "pending-latest" || c.checkpointPending {
		t.Fatal("maintenance did not flush current state")
	}
}

func TestCheckpointWriteFailureDoesNotSpin(t *testing.T) {
	c, state, path, now := checkpointFixture(t)
	if err := c.publishState(path, state, *now); err != nil {
		t.Fatal(err)
	}
	c.checkpointAt = time.Now().Add(-healthCheckpointInterval)
	c.checkpointPending = true
	c.checkpointNow = nil
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	// The Windows atomic-write fallback may remove an empty directory.
	if err := os.WriteFile(filepath.Join(path, "blocked"), []byte("retain directory"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Millisecond)
	defer cancel()
	if c.waitWithHealthCheckpoint(ctx, time.Second, nil, 10*time.Millisecond, nil, nil) {
		t.Fatal("unwritable checkpoint did not respect cancellation")
	}
	if !c.checkpointPending {
		t.Fatal("write failure discarded pending state")
	}
}

func BenchmarkHealthCheckpoint(b *testing.B) {
	for _, policies := range []int{1, 6} {
		for _, mode := range []string{"full", "batched"} {
			b.Run(fmt.Sprintf("%d-policies/%s", policies, mode), func(b *testing.B) {
				c, state, path, now := checkpointFixture(b)
				item := state["route"]
				for n := 0; n < 24; n++ {
					id := fmt.Sprintf("node-%02d", n)
					item.CandidateLabels[id] = id
					item.HistoryDays[id] = make(map[string]dayBucket)
					for day := 0; day < 7; day++ {
						item.HistoryDays[id][now.AddDate(0, 0, -day).Format("2006-01-02")] = dayBucket{Samples: 1440, Successes: 1440, LatencyObservations: 1440, LatencySamples: make([]int, 32)}
					}
					item.DailySamples[id] = make([]healthSample, 32)
				}
				for p := 1; p < policies; p++ {
					body, err := json.Marshal(item)
					if err != nil {
						b.Fatal(err)
					}
					var copy policyHealthState
					if err := json.Unmarshal(body, &copy); err != nil {
						b.Fatal(err)
					}
					state[fmt.Sprint(p)] = &copy
				}
				if err := c.publishState(path, state, *now); err != nil {
					b.Fatal(err)
				}
				encoded, _ := json.Marshal(state)
				b.ReportMetric(float64(len(encoded)), "history-B")
				b.ReportAllocs()
				b.ResetTimer()
				cpuBefore := int64(0)
				if checkpointCPUNanos != nil {
					cpuBefore = checkpointCPUNanos()
				}
				for i := 0; i < b.N; i++ {
					item.CheckedAt = fmt.Sprint(i)
					var err error
					if mode == "full" {
						err = c.publishState(path, state, *now)
					} else {
						err = c.publishRoutineState(path, state, *now)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				if checkpointCPUNanos != nil {
					b.ReportMetric(float64(checkpointCPUNanos()-cpuBefore)/float64(b.N), "cpu-ns/op")
				}
			})
		}
	}
}
