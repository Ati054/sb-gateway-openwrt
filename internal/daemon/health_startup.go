package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func startupRoutesConfirmed(body []byte, config settings.Config) bool {
	var state map[string]struct {
		Selected          string `json:"selected"`
		RuntimeSelected   string `json:"runtime_selected"`
		RuntimeConfirmed  bool   `json:"runtime_confirmed"`
		WANFallbackActive bool   `json:"wan_fallback_active"`
	}
	if json.Unmarshal(body, &state) != nil {
		return false
	}
	for _, policy := range config.Policies {
		item, ok := state["resproxy-"+policy.ID]
		if !ok || !item.RuntimeConfirmed || item.Selected == "" || item.Selected != item.RuntimeSelected || (policy.WANFallback && item.Selected == "block" && !item.WANFallbackActive) {
			return false
		}
	}
	return true
}

// Old update helpers use daemon.pid as their readiness signal. Publish it only
// after the new core's routes have been read back, including a confirmed block.
func waitStartupRoutes(ctx context.Context, config settings.Config, paths Paths, coreExited <-chan error, healthDone <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	path := filepath.Join(paths.RunRoot, "health-state", "selector-health.json")
	var observed os.FileInfo
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for route startup: %w", ctx.Err())
		case <-coreExited:
			return errors.New("core exited before routes became ready")
		case <-healthDone:
			return errors.New("health controller exited before routes became ready")
		default:
		}
		info, err := os.Stat(path)
		if err == nil && info.Size() <= 32<<20 && (observed == nil || !os.SameFile(info, observed) || !info.ModTime().Equal(observed.ModTime()) || info.Size() != observed.Size()) {
			observed = info
			if body, err := os.ReadFile(path); err == nil && startupRoutesConfirmed(body, config) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for route startup: %w", ctx.Err())
		case <-coreExited:
			return errors.New("core exited before routes became ready")
		case <-healthDone:
			return errors.New("health controller exited before routes became ready")
		case <-ticker.C:
		}
	}
}
