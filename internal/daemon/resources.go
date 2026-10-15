package daemon

import (
	"context"
	"encoding/json"
	"github.com/Ati054/sb-gateway-openwrt/internal/resources"
	"log"
	"os"
	"path/filepath"
	"time"
)

func monitorResources(ctx context.Context, paths Paths, core *supervisedCore) {
	timer := time.NewTicker(10 * time.Second)
	defer timer.Stop()
	lowBefore := false
	for {
		core.mu.RLock()
		pid := 0
		if core.current != nil {
			pid = core.current.pid
		}
		core.mu.RUnlock()
		state := resources.ReadSnapshot(pid)
		if body, err := os.ReadFile(filepath.Join(paths.RunRoot, "last-oom.json")); err == nil {
			var when time.Time
			if json.Unmarshal(body, &when) == nil {
				state.LastOOM = &when
			}
		}
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		body, err := core.Command(check, paths.Binary, "api", "statsquery", "--timeout=2")
		cancel()
		if err == nil {
			var counters []struct {
				Name  string `json:"name"`
				Value int64  `json:"value"`
			}
			if json.Unmarshal(body, &counters) == nil {
				for _, c := range counters {
					v := c.Value
					if v < 0 {
						continue
					}
					switch c.Name {
					case "sb-gateway>>>active>>>tcp":
						state.TCP = &v
					case "sb-gateway>>>active>>>udp":
						state.UDP = &v
					}
				}
			}
		}
		if state.LowMemory && !lowBefore {
			log.Printf("resproxy: low available router memory: %d KiB of %d KiB", state.AvailableKiB, state.TotalKiB)
		}
		lowBefore = state.LowMemory
		if body, err := json.Marshal(state); err == nil {
			// A small replaceable RAM file, no persistent logs or growing history.
			path := filepath.Join(paths.RunRoot, "resources.json")
			tmp, err := os.CreateTemp(paths.RunRoot, ".resources-*")
			if err == nil {
				name := tmp.Name()
				_, err = tmp.Write(body)
				closeErr := tmp.Close()
				if err == nil && closeErr == nil {
					_ = os.Rename(name, path)
				}
				_ = os.Remove(name)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
