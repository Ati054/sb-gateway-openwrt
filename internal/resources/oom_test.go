package resources

import (
	"testing"
)

func TestOOMAttributionRequiresPIDAndCurrentLifetime(t *testing.T) {
	record := "3,44,25000000,-;Out of memory: Killed process 123 (xray) total-vm:999kB"
	if !oomRecord(record, 123, 20) {
		t.Fatal("current OOM missed")
	}
	for _, x := range []struct {
		pid   int
		since float64
	}{{12, 20}, {123, 30}} {
		if oomRecord(record, x.pid, x.since) {
			t.Fatal("misattributed OOM")
		}
	}
	if oomRecord("Killed process 123 (xray)", 123, 20) {
		t.Fatal("unbounded timestamp accepted")
	}
}
