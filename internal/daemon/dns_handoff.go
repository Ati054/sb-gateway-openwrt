package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const httpsDNSProxyInit = "/etc/init.d/https-dns-proxy"

const dnsOwnerRecoveryBudget = 30 * time.Second

type dnsHandoffState struct {
	WasEnabled bool `json:"was_enabled"`
	WasRunning bool `json:"was_running"`
}

type dnsHandoffOps struct {
	installed      func() bool
	running        func() bool
	enabled        func() bool
	action         func(string) error
	err            func() error
	ensureRecovery func() error
	recovery       func() (dnsHandoffOps, context.CancelFunc)
}

func dnsHandoffFile(paths Paths) string {
	return filepath.Join(paths.DataRoot, "dns-handoff.json")
}

func httpsDNSProxyOps() dnsHandoffOps { return httpsDNSProxyOpsWithContext(context.Background()) }

func httpsDNSProxyOpsWithContext(parent context.Context) dnsHandoffOps {
	var stateError error
	call := func(action string) error {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		output, err := boundedCommand(ctx, httpsDNSProxyInit, action).CombinedOutput()
		if err != nil {
			if action == "running" || action == "enabled" {
				var exit *exec.ExitError
				if ctx.Err() != nil {
					stateError = ctx.Err()
				} else if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					stateError = err
				}
			}
			return fmt.Errorf("https-dns-proxy %s: %w: %s", action, err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	return dnsHandoffOps{
		recovery: func() (dnsHandoffOps, context.CancelFunc) {
			ctx, cancel := context.WithTimeout(context.Background(), dnsOwnerRecoveryBudget)
			return httpsDNSProxyOpsWithContext(ctx), cancel
		},
		ensureRecovery: ensureDNSBootRecovery,
		installed: func() bool {
			_, err := os.Stat(httpsDNSProxyInit)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				stateError = err
			}
			return err == nil
		},
		err:     func() error { return errors.Join(parent.Err(), stateError) },
		running: func() bool { return call("running") == nil },
		enabled: func() bool { return call("enabled") == nil },
		action:  call,
	}
}

// acquireDNSHandoff takes ownership only after explicit LuCI/UCI consent.
// A persisted marker makes an interrupted takeover recoverable after reboot.
func acquireDNSHandoff(path string, authorized bool, ops dnsHandoffOps) (acquired bool, resultErr error) {
	state, exists, err := readDNSHandoff(path)
	if err != nil {
		return false, err
	}
	if !ops.installed() {
		if err := dnsHandoffError(ops); err != nil {
			return false, err
		}
		if exists {
			return false, os.Remove(path)
		}
		return false, nil
	}
	if !exists {
		if !ops.running() || !authorized {
			return false, dnsHandoffError(ops)
		}
		if ops.ensureRecovery != nil {
			if err := ops.ensureRecovery(); err != nil {
				return false, err
			}
		}
		state = dnsHandoffState{WasEnabled: ops.enabled(), WasRunning: true}
		if err := dnsHandoffError(ops); err != nil {
			return false, err
		}
		if err := writeDNSHandoff(path, state); err != nil {
			return false, err
		}
	}
	// Once a durable marker exists, every failed acquisition must restore the
	// original owner. Fresh recovery probes must not inherit a latched status
	// error or cancellation from the failed acquisition.
	defer func() {
		if resultErr == nil {
			return
		}
		recovery := ops
		if ops.recovery != nil {
			var cancel context.CancelFunc
			recovery, cancel = ops.recovery()
			defer cancel()
		}
		if err := restoreDNSHandoff(path, recovery); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("restore previous DNS owner: %w", err))
		}
	}()
	running := ops.running()
	if err := dnsHandoffError(ops); err != nil {
		return false, err
	}
	if running {
		if err := ops.action("stop"); err != nil {
			return false, err
		}
		running = ops.running()
		if err := dnsHandoffError(ops); err != nil {
			return false, err
		}
		if running {
			return false, errors.New("https-dns-proxy is still running after stop")
		}
	}
	enabled := ops.enabled()
	if err := dnsHandoffError(ops); err != nil {
		return false, err
	}
	if enabled {
		if err := ops.action("disable"); err != nil {
			return false, err
		}
		enabled = ops.enabled()
		if err := dnsHandoffError(ops); err != nil {
			return false, err
		}
		if enabled {
			return false, errors.New("https-dns-proxy is still enabled after disable")
		}
	}
	if err := dnsHandoffError(ops); err != nil {
		return false, err
	}
	return true, nil
}

// restoreDNSHandoff returns exactly the service state that existed before
// takeover. It never deletes or edits the other package's configuration.
func restoreDNSHandoff(path string, ops dnsHandoffOps) error {
	state, exists, err := readDNSHandoff(path)
	if err != nil || !exists {
		return err
	}
	installed := ops.installed()
	if err := dnsHandoffError(ops); err != nil {
		return err
	}
	if !installed {
		return os.Remove(path)
	}
	enabled := ops.enabled()
	if err := dnsHandoffError(ops); err != nil {
		return err
	}
	if state.WasEnabled != enabled {
		action := "disable"
		if state.WasEnabled {
			action = "enable"
		}
		if err := ops.action(action); err != nil {
			return err
		}
	}
	running := ops.running()
	if err := dnsHandoffError(ops); err != nil {
		return err
	}
	if state.WasRunning != running {
		action := "stop"
		if state.WasRunning {
			action = "start"
		}
		if err := ops.action(action); err != nil {
			return err
		}
	}
	enabled = ops.enabled()
	if err := dnsHandoffError(ops); err != nil {
		return err
	}
	running = ops.running()
	if err := dnsHandoffError(ops); err != nil {
		return err
	}
	if state.WasEnabled != enabled || state.WasRunning != running {
		return errors.New("https-dns-proxy state could not be restored")
	}
	return os.Remove(path)
}
func dnsHandoffError(ops dnsHandoffOps) error {
	if ops.err != nil {
		return ops.err()
	}
	return nil
}

func readDNSHandoff(path string) (dnsHandoffState, bool, error) {
	content, err := readDNSMarker(path)
	if errors.Is(err, os.ErrNotExist) {
		return dnsHandoffState{}, false, nil
	}
	if err != nil {
		return dnsHandoffState{}, false, err
	}
	var state dnsHandoffState
	if err := json.Unmarshal(content, &state); err != nil {
		return dnsHandoffState{}, false, fmt.Errorf("invalid DNS handoff state: %w", err)
	}
	return state, true, nil
}

func writeDNSHandoff(path string, state dnsHandoffState) error {
	content, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeDurableDNSMarker(path, append(content, '\n'))
}
func writeDurableDNSMarker(path string, body []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".dns-handoff-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	if runtime.GOOS == "windows" {
		return nil
	}
	return directory.Sync()
}

func RestoreDNSHandoff(paths Paths) error {
	// One allowance covers the complete owner restoration, rather than a new
	// 30-second allowance for every status/action command. Leave time for the
	// independent 45-second managed-DNS cleanup inside procd's stop grace.
	return restoreDNSHandoffWithinBudget(dnsHandoffFile(paths), dnsOwnerRecoveryBudget, httpsDNSProxyOpsWithContext)
}

func restoreDNSHandoffWithinBudget(path string, budget time.Duration, operations func(context.Context) dnsHandoffOps) error {
	// Recovery must still run after cancellation of the startup context.
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	return restoreDNSHandoff(path, operations(ctx))
}

// Cleanup shares one deadline across both owned handoffs, reserving its final
// part for the previous owner even when dnsmasq exhausts its own allowance.
func RestoreManagedDNS(paths Paths) error {
	return restoreManagedDNSWithinBudget(paths, 45*time.Second, 15*time.Second)
}

func restoreManagedDNSWithinBudget(paths Paths, budget, ownerReserve time.Duration) error {
	return restoreDNSWithinBudget(context.Background(), budget, ownerReserve,
		func(ctx context.Context) error {
			return restoreDNSMasqHandoff(dnsmasqHandoffFile(paths), systemDNSMasqOpsWithContext(ctx))
		},
		func(ctx context.Context) error {
			return restoreDNSHandoff(dnsHandoffFile(paths), httpsDNSProxyOpsWithContext(ctx))
		})
}

func readDNSMarker(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, errors.New("invalid DNS handoff marker size or type")
	}
	body, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return nil, err
	}
	if len(body) > 65536 {
		return nil, errors.New("DNS handoff marker exceeds the limit")
	}
	return body, nil
}

// The recovery helper is independent of the package binary and is kept by
// sysupgrade. Avoid rewriting its identical contents on every service start.
func ensureDNSBootRecovery() error {
	body, err := readDNSMarker("/usr/share/resproxy/dns-boot-recover")
	if err != nil {
		return err
	}
	const destination = "/etc/uci-defaults/90-sb-gateway-dns-recover"
	if old, err := readDNSMarker(destination); err == nil && string(old) == string(body) {
		return nil
	}
	return writeDurableDNSMarker(destination, body)
}
