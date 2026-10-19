package resources

import (
	"runtime/debug"
	"testing"
)

func TestRuntimeSnapshotDoesNotChangeMemoryPolicy(t *testing.T) {
	limit := debug.SetMemoryLimit(-1)
	snapshot := ReadGoRuntime()
	if debug.SetMemoryLimit(-1) != limit || snapshot.MemoryLimitKiB != uint64(limit)/1024 {
		t.Fatal("runtime diagnostics changed the soft memory budget")
	}
	if snapshot.MemoryKiB == 0 || snapshot.MemoryKiB < snapshot.HeapAllocKiB || snapshot.GCCPUFraction < 0 || snapshot.GCCPUFraction > 1 {
		t.Fatal("invalid runtime memory/collector statistics")
	}
}
