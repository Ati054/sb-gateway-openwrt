package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/capture"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

const crashFailureLimit = 3
const crashStableSeconds = 300

type crashAttempt struct {
	Attempts  int   `json:"attempts"`
	StartedAt int64 `json:"started_at"`
	ReadyAt   int64 `json:"ready_at,omitempty"`
	PID       int   `json:"pid"`
}

func crashFile(paths Paths) string { return filepath.Join(paths.RunRoot, "crash-attempt.json") }
func crashFallbackFile(paths Paths) string {
	return filepath.Join(paths.RunRoot, "crash-fallback.active")
}
func CrashFallbackActive(paths Paths) bool {
	_, err := os.Stat(crashFallbackFile(paths))
	return err == nil
}

func beginCrashAttempt(paths Paths, now int64) (int, error) {
	if err := os.MkdirAll(paths.RunRoot, 0700); err != nil {
		return 0, err
	}
	var old crashAttempt
	body, err := os.ReadFile(crashFile(paths))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if len(body) > 4096 {
		return 0, errors.New("invalid crash attempt marker")
	}
	if len(body) > 0 && json.Unmarshal(body, &old) != nil {
		return 0, errors.New("invalid crash attempt marker")
	}
	attempts := old.Attempts
	// Use readiness, not lifetime since the last launch: a slow failing startup
	// must not continually reset its own failure allowance.
	if old.StartedAt > now || (old.ReadyAt > 0 && now-old.ReadyAt >= crashStableSeconds) {
		attempts = 0
	}
	attempts = min(max(attempts, 0)+1, crashFailureLimit+1)
	next := crashAttempt{Attempts: attempts, StartedAt: now, PID: os.Getpid()}
	encoded, _ := json.Marshal(next)
	return attempts, writeDurableDNSMarker(crashFile(paths), encoded)
}

func markCrashReady(paths Paths, now int64) error {
	body, err := os.ReadFile(crashFile(paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state crashAttempt
	if len(body) > 4096 || json.Unmarshal(body, &state) != nil {
		return errors.New("invalid crash attempt marker")
	}
	if state.PID != os.Getpid() {
		return nil
	}
	state.ReadyAt = now
	body, _ = json.Marshal(state)
	return writeDurableDNSMarker(crashFile(paths), body)
}

// EmergencyPolicies deliberately uses the list-wide database choice. An old
// per-domain fallback does not authorize all traffic from that source.
func EmergencyPolicies(config settings.Config) ([]capture.EmergencyPolicy, error) {
	policies := make([]capture.EmergencyPolicy, 0, len(config.Base.Policies)+1)
	var common *capture.EmergencyPolicy
	for _, p := range config.Base.Policies {
		behavior, err := p.ProxyOutageBehavior()
		if err != nil {
			return nil, err
		}
		scope := capture.EmergencyPolicy{SourceCIDRs: p.SourceCIDRs, AllowWAN: behavior == "wan"}
		if len(scope.SourceCIDRs) > 0 {
			policies = append(policies, scope)
		}
		if p.ID == config.Base.DefaultPolicy {
			copy := scope
			copy.SourceCIDRs = nil
			common = &copy
		}
	}
	if common == nil {
		return nil, errors.New("missing emergency default policy")
	}
	return append(policies, *common), nil
}

func applyCrashFallback(config settings.Config, paths Paths) error {
	if !config.Enabled || !config.RouteEnabled {
		return nil
	}
	if _, err := EmergencyPolicies(config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := setFirewall(ctx, "emergency", config.LANInterface, config.Base.TransparentPort, 0, config.Capture); err != nil {
		return err
	}
	// Publish only after nft accepted the complete table. Strict DNS is already
	// blocked before restoring the user's ordinary resolver.
	if err := writeDurableDNSMarker(crashFallbackFile(paths), []byte("source-policy WAN fallback\n")); err != nil {
		return err
	}
	if err := RestoreManagedDNS(paths); err != nil {
		return fmt.Errorf("emergency WAN active; restore DNS: %w", err)
	}
	log.Printf("resproxy: repeated service failures; emergency source-policy routing active")
	return nil
}

// Procd keeps ownership of the timer and process. Only small RAM markers are
// needed; no resident wrapper or third Go process is introduced.
func RunSupervised(ctx context.Context, config settings.Config, paths Paths) error {
	if os.Getenv("RESPROXY_PROCD_INSTANCE") != "gateway" {
		return Run(ctx, config, paths)
	}
	attempts, err := beginCrashAttempt(paths, time.Now().Unix())
	if err != nil {
		return err
	}
	if attempts > crashFailureLimit {
		if err := applyCrashFallback(config, paths); err != nil {
			log.Printf("resproxy: emergency recovery: %v", err)
		}
	}
	err = Run(ctx, config, paths)
	if ctx.Err() != nil {
		_ = os.Remove(crashFile(paths))
		_ = os.Remove(crashFallbackFile(paths))
	} else if attempts >= crashFailureLimit {
		if fallbackErr := applyCrashFallback(config, paths); fallbackErr != nil {
			log.Printf("resproxy: emergency recovery: %v", fallbackErr)
		}
	}
	return err
}
