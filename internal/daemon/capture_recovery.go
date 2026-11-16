package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/Ati054/xroute-openwrt/internal/capture"
	"github.com/Ati054/xroute-openwrt/internal/settings"
	"github.com/Ati054/xroute-openwrt/internal/xray"
)

func captureRecoveryPath(paths Paths) string {
	return filepath.Join(paths.DataRoot, "capture-recovery.json")
}

// One atomic record binds the kernel guard to its last working route graph.
// Initial startup may have an explicit guard but no working graph yet.
type networkRecovery struct {
	Version  int                 `json:"version"`
	Capture  CaptureRestoreInput `json:"capture"`
	Applied  *settings.Config    `json:"applied,omitempty"`
	Resolved *xray.BaseConfig    `json:"resolved,omitempty"`
}

// This plan contains only the acknowledged kernel routing contract. In
// particular, a broken or partly saved UCI edit cannot grant or revoke WAN
// access during recovery. No subscription, credential or node is serialized.
func saveCaptureRecovery(paths Paths, config settings.Config) error {
	if !config.Enabled || !config.RouteEnabled {
		return nil
	}
	body, err := EncodeCaptureRestore(config)
	if err != nil {
		return err
	}
	input, err := DecodeCaptureRestore(bytes.NewReader(body))
	if err != nil {
		return err
	}
	return writeNetworkRecovery(paths, networkRecovery{Version: 1, Capture: input})
}

func writeNetworkRecovery(paths Paths, state networkRecovery) error {
	return writeNetworkRecoverySynced(paths, state, syncMarkerDirectory)
}

func writeNetworkRecoverySynced(paths Paths, state networkRecovery, syncDirectory func(string) error) error {
	if _, err := recoveryRules(state.Capture); err != nil {
		return err
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(body) > 8<<20 {
		return errors.New("network recovery snapshot exceeds size limit")
	}
	if previous, err := os.ReadFile(captureRecoveryPath(paths)); err == nil && bytes.Equal(previous, body) {
		return nil
	}
	if err := os.MkdirAll(paths.DataRoot, 0700); err != nil {
		return err
	}
	renamed, err := writeSyncedMarker(captureRecoveryPath(paths), body, syncDirectory)
	if renamed && err != nil {
		// Runtime publication precedes this atomic replacement. Rolling it back
		// now would leave the visible recovery file describing the wrong plan.
		log.Printf("resproxy: recovery settings published; directory durability sync failed: %v", err)
		return nil
	}
	return err
}

func readNetworkRecovery(paths Paths) (networkRecovery, error) {
	path := captureRecoveryPath(paths)
	info, err := os.Lstat(path)
	if err != nil {
		return networkRecovery{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return networkRecovery{}, errors.New("invalid capture recovery plan")
	}
	file, err := os.Open(path)
	if err != nil {
		return networkRecovery{}, err
	}
	defer file.Close()
	var state networkRecovery
	decoder := json.NewDecoder(io.LimitReader(file, (8<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || state.Version != 1 || state.Capture.OwnerPID != 0 || !state.Capture.Enabled || !state.Capture.RouteEnabled {
		return state, errors.New("invalid network recovery ownership or version")
	}
	state.Capture.Policy.Automatic = capture.Inventory{}
	_, err = recoveryRules(state.Capture)
	return state, err
}

func ReadCaptureRecovery(paths Paths) (CaptureRestoreInput, error) {
	state, err := readNetworkRecovery(paths)
	return state.Capture, err
}

func recoveryRules(input CaptureRestoreInput) (string, error) {
	// The former managed DNS listener is dead; use the restored system DNS.
	input.Policy.DNSListenerPort = 0
	return capture.RenderEmergency(input.Interface, input.Port, input.Policy, input.Scopes)
}

// RenderCaptureRecovery is called under the firewall transaction lock. The
// current VPN inventory is discovered there, never restored from a snapshot.
func RenderCaptureRecovery(ctx context.Context, input CaptureRestoreInput) (string, error) {
	return renderCapturePolicy(ctx, input, "emergency", true)
}

func RenderCaptureProtection(ctx context.Context, input CaptureRestoreInput) (string, error) {
	return renderCapturePolicy(ctx, input, "guard", true)
}

func RenderBootstrapCapture(input CaptureRestoreInput) (string, error) {
	return renderCapturePolicy(context.Background(), input, "guard", false)
}

func renderCapturePolicy(ctx context.Context, input CaptureRestoreInput, mode string, discover bool) (string, error) {
	if !input.Enabled || !input.RouteEnabled || input.OwnerPID != 0 {
		return "", errors.New("invalid capture recovery input")
	}
	input.Policy.Automatic = capture.Inventory{}
	if discover {
		input.Policy.Automatic = capture.Discover(ctx)
		if !input.Policy.Automatic.Complete {
			return "", errors.New("VPN interface discovery unavailable; previous policy retained")
		}
	}
	body, err := recoveryRules(input)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# resproxy-restore %s %s %d 0\n%s", mode, input.Interface, input.Port, body), nil
}

func captureIntent(config settings.Config) bool {
	if len(config.Policies) == 0 {
		return false
	}
	if len(config.Subscriptions) > 0 {
		return true
	}
	for _, policy := range config.Base.Policies {
		if len(policy.Rules) > 0 {
			return true
		}
	}
	return false
}

// Removing the final subscription from an acknowledged route is not initial
// setup. Keep the route's outage policy and run an empty fail-closed pool.
func captureIntentWithHistory(config settings.Config, paths Paths) (bool, error) {
	if captureIntent(config) {
		return true, nil
	}
	if !config.RouteEnabled || len(config.Policies) == 0 {
		return false, nil
	}
	previous, err := readNetworkRecovery(paths)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if previous.Applied == nil {
		return false, nil
	}
	if !previous.Applied.Enabled || !previous.Applied.RouteEnabled || len(previous.Applied.Policies) == 0 {
		return false, errors.New("invalid acknowledged routing intent")
	}
	if err := previous.Applied.Base.Validate(); err != nil {
		return false, err
	}
	return true, nil
}

func bootstrapCapture(ctx context.Context, config settings.Config, paths Paths) error {
	input, err := ReadCaptureRecovery(paths)
	if errors.Is(err, os.ErrNotExist) {
		if !captureIntent(config) {
			return nil
		}
		if err := saveCaptureRecovery(paths, config); err != nil {
			return err
		}
		input, err = ReadCaptureRecovery(paths)
	}
	if err != nil {
		return err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return setFirewall(ctx, "bootstrap", input.Interface, input.Port, 0, input.Policy, body)
}

// Boot protection has no dependency on netifd/rpcd or a running core. Only
// configured source policies are protected; router OUTPUT remains untouched.
func BootstrapCapture(ctx context.Context, config settings.Config, paths Paths) error {
	if !config.Enabled || !config.RouteEnabled {
		return nil
	}
	lock, err := acquireController(paths)
	if err != nil {
		return err
	}
	defer lock.Close()
	return withCaptureLifecycle(ctx, paths, func() error { return bootstrapCapture(ctx, config, paths) })
}

func applyCaptureRecovery(ctx context.Context, paths Paths) error {
	input, err := ReadCaptureRecovery(paths)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !ProtectionActive(paths) && !CrashFallbackActive(paths) {
			if info, statErr := os.Stat(filepath.Join(paths.RunRoot, "firewall.nft")); errors.Is(statErr, os.ErrNotExist) || statErr == nil && info.Size() == 0 {
				return nil // Nothing was configured or captured; preserve normal WAN.
			}
		}
		return err
	}
	// Re-encode the checked snapshot, rather than reopening a mutable file in
	// the child or reading the UCI configuration that caused this failure.
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return setFirewall(ctx, "recovery", input.Interface, input.Port, 0, input.Policy, body)
}
