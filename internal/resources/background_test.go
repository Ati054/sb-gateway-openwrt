package resources

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackgroundCPUDefersScansAndResumesWithHeadroom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stat")
	now := time.Unix(100, 0)
	updating := false
	gate := &backgroundGate{path: path, now: func() time.Time { return now }, updating: func() bool { return updating }}
	write := func(user, idle int) {
		t.Helper()
		if err := os.WriteFile(path, []byte(fmt.Sprintf("cpu %d 0 0 %d 0 0 0 0\n", user, idle)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(100, 100)
	if gate.read() {
		t.Fatal("first CPU observation is not saturation evidence")
	}
	now = now.Add(time.Second)
	write(200, 100)
	if !gate.read() {
		t.Fatal("optional scan admitted at 100% CPU")
	}
	write(200, 200)
	now = now.Add(100 * time.Millisecond)
	if !gate.read() {
		t.Fatal("per-policy calls discarded the sampled load")
	}
	now = now.Add(time.Second)
	if gate.read() {
		t.Fatal("quality scans did not resume after CPU pressure")
	}
	updating = true
	if !gate.read() {
		t.Fatal("maintenance overlap admitted")
	}
	updating = false
	now = now.Add(time.Second)
	write(1, 1)
	if gate.read() {
		t.Fatal("CPU counter reset treated as overload")
	}
	now = now.Add(time.Second)
	os.Remove(path)
	if gate.read() {
		t.Fatal("missing CPU data indefinitely blocked quality work")
	}
}
