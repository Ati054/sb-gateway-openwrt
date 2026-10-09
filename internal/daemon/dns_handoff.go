package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
}

func dnsHandoffFile(paths Paths) string {
	return filepath.Join(paths.DataRoot, "dns-handoff.json")
}

func httpsDNSProxyOps() dnsHandoffOps {
	call := func(action string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, httpsDNSProxyInit, action).CombinedOutput()
		if err != nil {
			return fmt.Errorf("https-dns-proxy %s: %w: %s", action, err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	return dnsHandoffOps{
		installed: func() bool { _, err := os.Stat(httpsDNSProxyInit); return err == nil },
		running:   func() bool { return call("running") == nil },
		enabled:   func() bool { return call("enabled") == nil },
		action:    call,
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
		if exists {
			return false, os.Remove(path)
		}
		return false, nil
	}
	if !exists {
		if !ops.running() || !authorized {
			return false, nil
		}
		state = dnsHandoffState{WasEnabled: ops.enabled(), WasRunning: true}
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
	return os.Remove(path)
}

func readDNSHandoff(path string) (dnsHandoffState, bool, error) {
	content, err := os.ReadFile(path)
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
	temporary := path + ".new"
	if err := os.WriteFile(temporary, append(content, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func RestoreDNSHandoff(paths Paths) error {
	return restoreDNSHandoff(dnsHandoffFile(paths), httpsDNSProxyOps())
}
