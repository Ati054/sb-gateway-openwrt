package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Ati054/xroute-openwrt/internal/capture"
)

func startupFailureFile(paths Paths) string {
	return filepath.Join(paths.RunRoot, "startup-failed.active")
}

func StartupFailed(paths Paths) bool {
	_, err := os.Stat(startupFailureFile(paths))
	return err == nil
}

// Offline subscription imports share the catalog, but do not own networking.
// Keep network recovery independent from their potentially slow downloads.
func acquireController(paths Paths) (*os.File, error) {
	if err := os.MkdirAll(paths.RunRoot, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(paths.RunRoot, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockCatalogFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("another controller owns LAN routing: %w", err)
	}
	return file, nil
}

func plannedStop(paths Paths) bool {
	body, err := os.ReadFile(filepath.Join(paths.RunRoot, "stopping.pid"))
	if err != nil || len(body) > 4096 {
		return false
	}
	for _, pid := range strings.Fields(string(body)) {
		if pid == strconv.Itoa(os.Getpid()) {
			return true
		}
	}
	return false
}

func recoverServiceFailure(paths Paths, runErr error, rollback func() error) error {
	// SIGTERM can come from procd's watchdog, with no init cleanup callback.
	// Only an explicit init transition may inherit capture from an exiting daemon.
	if runErr != nil && !plannedStop(paths) {
		return errors.Join(runErr, rollback())
	}
	return runErr
}

// Serialize the decision as well as nft publication. Otherwise a hotplug that
// observed an old guard can reinstall it after Stop has removed interception.
func withCaptureLifecycle(ctx context.Context, paths Paths, change func() error) error {
	if err := os.MkdirAll(paths.RunRoot, 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(paths.RunRoot, "capture-lifecycle.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := lockCatalogFile(file); err == nil {
			return change()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func removeCaptureState(paths Paths, remove func() error) error {
	if err := remove(); err != nil {
		return err
	}
	result := restoreBridgeNetfilter(paths, bridgeSysctlRoot)
	for _, path := range []string{protectionFile(paths), crashFallbackFile(paths), dnsInteropPath(paths), configurationRecoveryPath(paths)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	return result
}

// ReleaseCapture is also used by init after the old daemon has fully exited.
func ReleaseCapture(paths Paths) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return withCaptureLifecycle(ctx, paths, func() error {
		if err := removeCaptureState(paths, func() error { return setFirewall(ctx, "remove", "", 0, 0, capture.Policy{}) }); err != nil {
			return err
		}
		result := os.Remove(startupFailureFile(paths))
		if errors.Is(result, os.ErrNotExist) {
			result = nil
		}
		if paths.DataRoot != "" {
			for _, path := range []string{captureRecoveryPath(paths)} {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					result = errors.Join(result, err)
				}
			}
		}
		return result
	})
}

func rollbackStartup(paths Paths, replace func(context.Context) error, restoreDNS func() error) error {
	// Startup's context is already cancelled on a timeout or fatal worker error.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	captureErr := withCaptureLifecycle(ctx, paths, func() error {
		if err := writeDurableDNSMarker(startupFailureFile(paths), []byte("service failed; acknowledged source policy retained\n")); err != nil {
			return err
		}
		if err := replace(ctx); err != nil {
			return err
		}
		result := restoreBridgeNetfilter(paths, bridgeSysctlRoot)
		for _, path := range []string{protectionFile(paths), dnsInteropPath(paths)} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
		return result
	})
	return errors.Join(captureErr, restoreDNS())
}

func rollbackStartupNetwork(paths Paths, stage string) error {
	removed, restored := false, false
	err := rollbackStartup(paths, func(ctx context.Context) error {
		if err := applyCaptureRecovery(ctx, paths); err != nil {
			// Unreadable policy is not permission to open strict traffic. Keep
			// the kernel rules and report the recovery error instead.
			return err
		}
		removed = true // Dead TProxy delivery has been replaced, not all policy.
		return nil
	}, func() error { err := RestoreManagedDNS(paths); restored = err == nil; return err })
	bridgeState, bridgeErr := readBridgeState(paths)
	if recordErr := recordServiceFailure(paths, stage, removed, restored, bridgeErr == nil && len(bridgeState) == 0); recordErr != nil {
		log.Printf("resproxy: could not save incident recovery status: %v", recordErr)
	}
	if err == nil {
		log.Printf("resproxy: service failed; acknowledged WAN/strict source policy retained and previous DNS restored")
	}
	return err
}

// A malformed UCI snapshot fails before Run. Recover its early init guard too,
// but never tear down a different live daemon's network.
func RollbackStartup(paths Paths) error {
	lock, err := acquireController(paths)
	if err != nil {
		return err
	}
	defer lock.Close()
	rules, _ := os.Stat(filepath.Join(paths.RunRoot, "firewall.nft"))
	_, bridgeErr := os.Stat(bridgeStatePath(paths))
	if !ProtectionActive(paths) && !CrashFallbackActive(paths) && !StartupFailed(paths) && errors.Is(bridgeErr, os.ErrNotExist) && (rules == nil || rules.Size() == 0) {
		return nil
	}
	return rollbackStartupNetwork(paths, "configuration")
}

func RecoverStartupFailure(paths Paths, cause error) error {
	return recoverServiceFailure(paths, cause, func() error { return RollbackStartup(paths) })
}
