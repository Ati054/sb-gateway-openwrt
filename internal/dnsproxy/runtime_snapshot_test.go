package dnsproxy

import (
	"encoding/json"
	"github.com/Ati054/sb-gateway-openwrt/internal/healthcontract"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func runtimeFixture(t testing.TB, path, selected string, at time.Time) {
	t.Helper()
	body, err := json.Marshal(healthcontract.RuntimeSnapshot{UpdatedAt: at, Policies: map[string]healthcontract.RuntimePolicy{
		"resproxy-main": {Selected: selected, RuntimeSelected: selected, RuntimeConfirmed: true, WANFallbackActive: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".new", body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSnapshotCacheLeaseAtomicReplacementAndInvalidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	s := &server{config: Config{HealthStatePath: path}}
	runtimeFixture(t, path, "block", time.Now())
	s.readState()
	if !s.stateValid || !s.ruleWANActive["resproxy-main"] {
		t.Fatal("confirmed outage missing")
	}
	cached := s.stateFileInfo
	s.readState()
	if s.stateFileInfo != cached {
		t.Fatal("unchanged runtime was reopened and parsed")
	}
	// Equal-size/equal-mtime replacement must still be read. Production writers
	// publish with rename, never in place; inode identity is part of the cache key.
	runtimeFixture(t, path, "proxy", time.Now())
	if err := os.Chtimes(path, cached.ModTime(), cached.ModTime()); err != nil {
		t.Fatal(err)
	}
	s.readState()
	if s.ruleWANActive["resproxy-main"] || s.proxySelections["resproxy-main"] != "proxy" {
		t.Fatal("missed equal-size/mtime atomic replacement")
	}
	runtimeFixture(t, path, "block", time.Now())
	s.readState()
	s.stateLease = time.Now().Add(-time.Minute)
	s.readState()
	// Expired embedded lease cannot be renewed just by fresh filesystem metadata.
	runtimeFixture(t, path, "block", time.Now().Add(-time.Minute))
	s.readState()
	if s.stateValid {
		t.Fatal("fresh mtime authorized an expired embedded lease")
	}
	runtimeFixture(t, path, "block", time.Now())
	s.readState()
	old := time.Now().Add(-time.Minute)
	os.Chtimes(path, old, old)
	s.readState()
	if s.stateValid {
		t.Fatal("cached WAN survived stale file")
	}
	runtimeFixture(t, path, "block", time.Now())
	s.readState()
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	s.readState()
	if s.stateValid {
		t.Fatal("malformed snapshot retained WAN")
	}
	runtimeFixture(t, path, "block", time.Now())
	s.readState()
	os.Remove(path)
	s.readState()
	if s.stateValid {
		t.Fatal("deleted snapshot retained WAN")
	}
	runtimeFixture(t, path, "block", time.Now().Add(time.Minute))
	s.readState()
	if s.stateValid {
		t.Fatal("future-dated snapshot authorized WAN")
	}
}

func BenchmarkDNSStatePolling(b *testing.B) {
	root := b.TempDir()
	compact := filepath.Join(root, "runtime.json")
	runtimeFixture(b, compact, "block", time.Now())
	s := &server{config: Config{HealthStatePath: compact}}
	s.readState()
	b.Run("UnchangedCompact", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s.readState()
		}
	})
	// Baseline: the previous reader scanned all history to extract four fields.
	payload := map[string]any{"resproxy-main": map[string]any{"selected": "block", "runtime_selected": "block", "runtime_confirmed": true, "wan_fallback_active": true, "history_days": map[string]any{}}}
	days := payload["resproxy-main"].(map[string]any)["history_days"].(map[string]any)
	for i := 0; i < 100; i++ {
		days[time.Unix(int64(i), 0).Format(time.RFC3339)] = make([]int, 224)
	}
	body, _ := json.Marshal(payload)
	history := filepath.Join(root, "history.json")
	os.WriteFile(history, body, 0600)
	b.Run("PreviousHistoryRead", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		for i := 0; i < b.N; i++ {
			body, err := os.ReadFile(history)
			if err != nil {
				b.Fatal(err)
			}
			var values map[string]healthcontract.RuntimePolicy
			if err := json.Unmarshal(body, &values); err != nil {
				b.Fatal(err)
			}
		}
	})
}
