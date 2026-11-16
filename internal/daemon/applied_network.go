package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"

	"github.com/Ati054/xroute-openwrt/internal/settings"
	"github.com/Ati054/xroute-openwrt/internal/xray"
)

func configurationRecoveryPath(paths Paths) string {
	return filepath.Join(paths.RunRoot, "configuration-recovery.json")
}
func ConfigurationRecovered(paths Paths) bool {
	_, err := os.Stat(configurationRecoveryPath(paths))
	return err == nil
}

// A recovery run serves the durable catalog without downloading subscriptions.
// Source IDs remain intact so recovery cannot prune that catalog accidentally.
func saveAppliedNetwork(paths Paths, config settings.Config, resolved ...xray.BaseConfig) error {
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
	base := config.Base
	if len(resolved) > 0 {
		base = resolved[0]
	} else if previous, err := readNetworkRecovery(paths); err == nil && previous.Applied != nil && previous.Resolved != nil &&
		(reflect.DeepEqual(previous.Applied.Base, config.Base) || reflect.DeepEqual(*previous.Resolved, config.Base)) {
		base = *previous.Resolved
		config.Base = previous.Applied.Base
	}
	if err := base.Validate(); err != nil {
		return err
	}
	config.Capture.Automatic = input.Policy.Automatic
	config.SubscriptionURL = ""
	config.Subscriptions = append([]settings.Subscription(nil), config.Subscriptions...)
	for i := range config.Subscriptions {
		config.Subscriptions[i].URL = ""
		config.Subscriptions[i].UpdateMode = "manual"
	}
	return writeNetworkRecovery(paths, networkRecovery{Version: 1, Capture: input, Applied: &config, Resolved: &base})
}

func readAppliedNetwork(paths Paths) (settings.Config, error) {
	state, err := readNetworkRecovery(paths)
	if err != nil {
		return settings.Config{}, err
	}
	if state.Applied == nil {
		return settings.Config{}, errors.New("no confirmed network configuration")
	}
	config := *state.Applied
	if !config.Enabled || !config.RouteEnabled {
		return config, errors.New("invalid applied network state")
	}
	if err := config.Base.Validate(); err != nil {
		return config, err
	}
	if config.SubscriptionURL != "" {
		return config, errors.New("recovery must use the durable catalog")
	}
	for _, source := range config.Subscriptions {
		if source.URL != "" || source.UpdateMode != "manual" {
			return config, errors.New("recovery must not download subscriptions")
		}
	}
	input, err := EncodeCaptureRestore(config)
	if err != nil {
		return config, err
	}
	expected, err := json.Marshal(state.Capture)
	if err != nil || !bytes.Equal(input, expected) {
		return config, errors.New("applied network and capture generations differ")
	}
	if state.Resolved == nil {
		return config, errors.New("resolved recovery routing unavailable")
	}
	config.Base = *state.Resolved
	if err := config.Base.Validate(); err != nil {
		return config, err
	}
	resolvedCapture, err := EncodeCaptureRestore(config)
	if err != nil || !bytes.Equal(resolvedCapture, expected) {
		return config, errors.New("resolved recovery policy differs from capture")
	}
	return config, nil
}

func refreshResolvedNetwork(ctx context.Context, paths Paths, expected xray.BaseConfig) error {
	return withCaptureLifecycle(ctx, paths, func() error {
		state, err := readNetworkRecovery(paths)
		if err != nil {
			return err
		}
		if state.Applied == nil || !reflect.DeepEqual(state.Applied.Base, expected) {
			return nil
		}
		// The ruleset snapshot is persisted only after the core and DNS have
		// accepted it. Resolve that committed generation inside our own lock.
		base, err := withRulesets(state.Applied.Base, state.Applied.Policies, loadRulesetSnapshot(paths), filepath.Join(paths.DataRoot, "geoip-assets"))
		if err != nil {
			return err
		}
		return saveAppliedNetwork(paths, *state.Applied, base)
	})
}

func acknowledgeConfiguration(paths Paths) {
	if err := os.Remove(configurationRecoveryPath(paths)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("resproxy: could not clear configuration recovery status: %v", err)
		return
	}
	if err := markServiceRecovered(paths); err != nil {
		log.Printf("resproxy: could not save recovered incident status: %v", err)
	}
}

// Run has completely unwound and released its controller lock before this is
// called. Recovery uses the same single-core lifecycle and exact route graph.
func ResumeAppliedNetwork(ctx context.Context, paths Paths, cause error) error {
	if ctx.Err() != nil || plannedStop(paths) {
		return cause
	}
	flags, err := settings.ReadProtectionSystemUCI(ctx)
	if err != nil || !flags.Enabled || !flags.RouteEnabled {
		return errors.Join(cause, err)
	}
	config, err := readAppliedNetwork(paths)
	if err != nil {
		return errors.Join(cause, err)
	}
	log.Printf("resproxy: new configuration unavailable; restoring last applied network settings")
	paths.recoveringConfiguration = true
	return errors.Join(cause, Run(ctx, config, paths))
}
