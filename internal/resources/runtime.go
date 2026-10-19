package resources

import (
	"runtime"
	"runtime/debug"
	"runtime/metrics"
)

// GoRuntime separates the collector's working set from mapped executable
// pages and exposes whether a soft limit is consuming CPU in collection.
type GoRuntime struct {
	MemoryKiB      uint64  `json:"memory_kib"`
	HeapLiveKiB    uint64  `json:"heap_live_kib"`
	HeapAllocKiB   uint64  `json:"heap_alloc_kib"`
	HeapGoalKiB    uint64  `json:"heap_goal_kib"`
	MemoryLimitKiB uint64  `json:"memory_limit_kib"`
	GCCycles       uint32  `json:"gc_cycles"`
	GCLimiterCycle uint64  `json:"gc_limiter_last_cycle"`
	GCCPUFraction  float64 `json:"gc_cpu_fraction"`
}

func ReadGoRuntime() *GoRuntime {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	samples := []metrics.Sample{{Name: "/gc/heap/live:bytes"}, {Name: "/gc/limiter/last-enabled:gc-cycle"}}
	metrics.Read(samples)
	state := &GoRuntime{MemoryKiB: (memory.Sys - memory.HeapReleased) / 1024,
		HeapAllocKiB: memory.HeapAlloc / 1024, HeapGoalKiB: memory.NextGC / 1024,
		MemoryLimitKiB: uint64(debug.SetMemoryLimit(-1)) / 1024,
		GCCycles:       memory.NumGC, GCCPUFraction: memory.GCCPUFraction}
	if samples[0].Value.Kind() == metrics.KindUint64 {
		state.HeapLiveKiB = samples[0].Value.Uint64() / 1024
	}
	if samples[1].Value.Kind() == metrics.KindUint64 {
		state.GCLimiterCycle = samples[1].Value.Uint64()
	}
	return state
}
