package agentport

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestHistoryReservoirContinuesReplacingAfter12345Observations(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	key := now.Format("2006-01-02")
	values := make([]int, historyReservoir)
	for i := range values {
		values[i] = 100
	}
	days := map[string]dayBucket{key: {Samples: 20000, Successes: 20000, LatencyObservations: 20000, LatencySamples: values}}
	delay, calls := 900, 0
	appendHistoryDayWithSampler(days, healthSample{At: float64(now.Unix()), OK: true, DelayMS: &delay}, func(n int) int {
		calls++
		if n != 20001 {
			t.Fatalf("reservoir population=%d, want 20001", n)
		}
		return 0 // A valid late observation can replace the first retained value.
	})
	got := days[key]
	if calls != 1 || got.LatencySamples[0] != delay || len(got.LatencySamples) != historyReservoir || got.LatencyObservations != 20001 {
		t.Fatalf("late observation was not sampled: %+v, draws=%d", got, calls)
	}
	if got.Samples != 20001 || got.Successes != 20001 || got.Failures != 0 {
		t.Fatalf("reservoir changed availability counters: %+v", got)
	}
}

func TestHistoryReservoirPopulationExcludesUnmeasuredSuccessesAndFailures(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	key := now.Format("2006-01-02")
	days := make(map[string]dayBucket)
	noDraw := func(int) int { t.Fatal("non-latency or initial sample requested a reservoir draw"); return 0 }
	for i := 0; i < 100; i++ {
		appendHistoryDayWithSampler(days, healthSample{At: float64(now.Unix()), OK: true}, noDraw)
	}
	for i := 0; i < 20; i++ {
		appendHistoryDayWithSampler(days, healthSample{At: float64(now.Unix())}, noDraw)
	}
	for i := 0; i < historyReservoir; i++ {
		delay := i + 1
		appendHistoryDayWithSampler(days, healthSample{At: float64(now.Unix()), OK: true, DelayMS: &delay}, noDraw)
	}
	before := append([]int(nil), days[key].LatencySamples...)
	delay, calls := 999, 0
	appendHistoryDayWithSampler(days, healthSample{At: float64(now.Unix()), OK: true, DelayMS: &delay}, func(n int) int {
		calls++
		if n != historyReservoir+1 {
			t.Fatalf("unmeasured results entered latency population: %d", n)
		}
		return historyReservoir // This valid draw retains the existing reservoir.
	})
	got := days[key]
	if calls != 1 || got.LatencyObservations != historyReservoir+1 || !reflect.DeepEqual(before, got.LatencySamples) {
		t.Fatalf("sampling population or retention changed: %+v", got)
	}
	if got.Samples != 121+historyReservoir || got.Successes != 101+historyReservoir || got.Failures != 20 {
		t.Fatalf("availability counters changed: %+v", got)
	}
}

func TestHistoryReservoirMigratesLegacyBucketsWithoutDiscardingData(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	key := now.Format("2006-01-02")
	for _, size := range []int{3, historyReservoir} {
		t.Run(fmt.Sprintf("retained=%d", size), func(t *testing.T) {
			values := make([]int, size)
			for i := range values {
				values[i] = 100 + i
			}
			days := map[string]dayBucket{key: {Samples: 20010, Successes: 20000, Failures: 10, LatencySamples: values}}
			before := append([]int(nil), values...)
			// A non-latency success initializes legacy metadata without inventing a value.
			appendHistoryDayWithSampler(days, healthSample{At: float64(now.Unix()), OK: true}, func(int) int { t.Fatal("migration sampled without a latency"); return 0 })
			population := size
			if size == historyReservoir {
				population = 20000
			}
			got := days[key]
			if got.LatencyObservations != population || !reflect.DeepEqual(before, got.LatencySamples) || got.Successes != 20001 || got.Failures != 10 {
				t.Fatalf("legacy history changed: %+v", got)
			}
			delay := 900
			appendHistoryDayWithSampler(days, healthSample{At: float64(now.Unix()), OK: true, DelayMS: &delay}, func(n int) int {
				if n != population+1 {
					t.Fatalf("legacy population=%d, want %d", n, population+1)
				}
				return 0
			})
			got = days[key]
			if got.LatencyObservations != population+1 {
				t.Fatal("migrated population did not advance")
			}
			if size == historyReservoir && got.LatencySamples[0] != delay {
				t.Fatal("legacy saturated reservoir remained frozen")
			}
			if size < historyReservoir && (len(got.LatencySamples) != size+1 || got.LatencySamples[size] != delay) {
				t.Fatal("legacy initial reservoir lost values")
			}
		})
	}
}

func TestHistoryReservoirCounterSurvivesCheckpointRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	key := now.Format("2006-01-02")
	days := make(map[string]dayBucket)
	for i := 0; i < historyReservoir; i++ {
		delay := i + 1
		appendHistoryDay(days, healthSample{At: float64(now.Unix()), OK: true, DelayMS: &delay})
	}
	body, err := json.Marshal(days)
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]dayBucket
	if err := json.Unmarshal(body, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(days, restored) {
		t.Fatal("checkpoint lost reservoir metadata")
	}
	appendHistoryDayWithSampler(restored, healthSample{At: float64(now.Unix()), OK: true}, func(int) int { t.Fatal("unmeasured success was sampled"); return 0 })
	delay := 800
	appendHistoryDayWithSampler(restored, healthSample{At: float64(now.Unix()), OK: true, DelayMS: &delay}, func(n int) int {
		if n != historyReservoir+1 {
			t.Fatalf("restored population=%d", n)
		}
		return 0
	})
	if restored[key].LatencySamples[0] != delay || restored[key].LatencyObservations != historyReservoir+1 {
		t.Fatal("restored reservoir did not resume sampling")
	}
}
