package daemon

import (
	"os"
	"testing"
)

func TestIncidentRecordSurvivesRetryWithoutRepeatedFlashWrites(t *testing.T) {
	paths := Paths{DataRoot: t.TempDir()}
	if err := recordServiceFailure(paths, "runtime", true, true, true); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(failureRecordPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if err := recordServiceFailure(paths, "configuration", true, true, true); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(failureRecordPath(paths))
	if string(first) != string(again) {
		t.Fatal("respawn rewrote the incident")
	}
	if err := markServiceRecovered(paths); err != nil {
		t.Fatal(err)
	}
	if record := LastServiceFailure(paths); record == nil || record.RecoveredAt == "" || !record.CaptureRemoved || !record.DNSRestored {
		t.Fatal("lost incident or recovery status", record)
	}
	if err := recordServiceFailure(paths, "runtime", false, true, true); err != nil {
		t.Fatal(err)
	}
	if record := LastServiceFailure(paths); record == nil || record.RecoveredAt != "" || record.CaptureRemoved {
		t.Fatal("new incident reused the old recovered record", record)
	}
}
