package agentport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSharedHealthPoolGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	stamp := time.Unix(1700000000, 0)
	publish := func(version int, id string) {
		t.Helper()
		body, err := json.Marshal(healthPool{Version: version, Outbounds: map[string]json.RawMessage{id: json.RawMessage(`{"protocol":"vless"}`)}})
		if err != nil {
			t.Fatal(err)
		}
		next := path + ".next"
		if err := os.WriteFile(next, body, 0600); err != nil {
			t.Fatal(err)
		}
		// Preserve size and mtime across atomic replacement; file identity must invalidate the cache.
		if err := os.Chtimes(next, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(next, path); err != nil {
			t.Fatal(err)
		}
	}
	publish(4, "first")
	cache := &healthPoolCache{}
	workers := make([]*xraySelectorRuntime, 11)
	var join sync.WaitGroup
	for i := range workers {
		workers[i] = newXraySelectorRuntime(Options{HealthPoolFile: path})
		workers[i].poolCache = cache
		join.Add(1)
		go func(lane *xraySelectorRuntime) {
			defer join.Done()
			if _, reset, err := lane.Reload(); err != nil || !reset {
				t.Errorf("first generation: reset=%v err=%v", reset, err)
			}
		}(workers[i])
	}
	join.Wait()
	first := workers[0].pool.Outbounds["first"]
	for _, lane := range workers {
		if &first[0] != &lane.pool.Outbounds["first"][0] {
			t.Fatal("inventory was duplicated")
		}
		if _, reset, err := lane.Reload(); err != nil || reset {
			t.Fatalf("unchanged: reset=%v err=%v", reset, err)
		}
	}
	workers[0].loadedDynamic["private-handler"] = true
	if workers[1].loadedDynamic["private-handler"] {
		t.Fatal("handler ownership leaked across lanes")
	}
	publish(2, "invalid")
	if _, _, err := workers[0].Reload(); err == nil {
		t.Fatal("invalid generation accepted")
	}
	if workers[0].pool.Outbounds["first"] == nil {
		t.Fatal("invalid generation replaced inventory")
	}
	publish(4, "other")
	for _, lane := range workers {
		if _, reset, err := lane.Reload(); err != nil || !reset {
			t.Fatalf("replacement: reset=%v err=%v", reset, err)
		}
		if lane.pool.Outbounds["first"] != nil || lane.pool.Outbounds["other"] == nil {
			t.Fatal("lane retained obsolete inventory")
		}
	}
	// Already running jobs may retain an old immutable generation safely.
	if string(first) != `{"protocol":"vless"}` {
		t.Fatal("old snapshot mutated")
	}
}
