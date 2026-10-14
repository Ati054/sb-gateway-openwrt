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

type dnsHandoffState struct {
	WasEnabled bool `json:"was_enabled"`
	WasRunning bool `json:"was_running"`
}

type dnsHandoffOps struct {
	installed func() bool
	running   func() bool
	enabled   func() bool
	action    func(string) error
	err       func() error
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
func acquireDNSHandoff(path string, authorized bool, ops dnsHandoffOps) (bool, error) {
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
		state = dnsHandoffState{WasEnabled: ops.enabled(), WasRunning: true}
		if err := dnsHandoffError(ops); err != nil {
			return false, err
		}
		if err := writeDNSHandoff(path, state); err != nil {
			return false, err
		}
	}
	if ops.running() {
		if err := ops.action("stop"); err != nil {
			_ = restoreDNSHandoff(path, ops)
			return false, err
		}
		if ops.running() {
			_ = restoreDNSHandoff(path, ops)
			return false, errors.New("https-dns-proxy is still running after stop")
		}
	}
	if ops.enabled() {
		if err := ops.action("disable"); err != nil {
			_ = restoreDNSHandoff(path, ops)
			return false, err
		}
		if ops.enabled() {
			_ = restoreDNSHandoff(path, ops)
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
	if !ops.installed() {
		if err := dnsHandoffError(ops); err != nil {
			return err
		}
		return os.Remove(path)
	}
	if state.WasEnabled != ops.enabled() {
		action := "disable"
		if state.WasEnabled {
			action = "enable"
		}
		if err := ops.action(action); err != nil {
			return err
		}
	}
	if state.WasRunning != ops.running() {
		action := "stop"
		if state.WasRunning {
			action = "start"
		}
		if err := ops.action(action); err != nil {
			return err
		}
	}
	if state.WasEnabled != ops.enabled() || state.WasRunning != ops.running() {
		return errors.New("https-dns-proxy state could not be restored")
	}
	if err := dnsHandoffError(ops); err != nil {
		return err
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
	return restoreDNSHandoff(dnsHandoffFile(paths), httpsDNSProxyOps())
}

// Cleanup shares one deadline across both owned handoffs. A failure restoring
// dnsmasq must not prevent attempting to restart the user's previous resolver.
func RestoreManagedDNS(paths Paths) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return errors.Join(restoreDNSMasqHandoff(dnsmasqHandoffFile(paths), systemDNSMasqOpsWithContext(ctx)), restoreDNSHandoff(dnsHandoffFile(paths), httpsDNSProxyOpsWithContext(ctx)))
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
