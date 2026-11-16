package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const bridgeSysctlRoot = "/proc/sys/net/bridge"

var bridgeCaptureFlags = [...]string{"bridge-nf-call-iptables", "bridge-nf-call-ip6tables"}

func bridgeStatePath(paths Paths) string {
	return filepath.Join(paths.RunRoot, "bridge-netfilter.json")
}

func readBridgeState(paths Paths) (map[string]int, error) {
	file, err := os.Open(bridgeStatePath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]int{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return nil, err
	}
	state := map[string]int{}
	if len(body) > 4096 || json.Unmarshal(body, &state) != nil || state == nil {
		return nil, errors.New("invalid bridge netfilter ownership record")
	}
	for name, value := range state {
		if (name != bridgeCaptureFlags[0] && name != bridgeCaptureFlags[1]) || value != 1 {
			return nil, errors.New("invalid bridge netfilter ownership record")
		}
	}
	return state, nil
}

func bridgeFlag(root, name string) (string, error) {
	body, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(body))
	if value != "0" && value != "1" {
		return "", fmt.Errorf("unexpected bridge netfilter value: %s", name)
	}
	return value, nil
}

// Caller holds the capture lifecycle lock. Bridged IPv4/IPv6 netfilter can
// interfere with LAN TProxy delivery. Change only enabled flags, recording
// ownership before the first write so partial startup can restore them too.
func prepareBridgeNetfilter(paths Paths, root string) error {
	state, err := readBridgeState(paths)
	if err != nil {
		return err
	}
	var change []string
	for _, name := range bridgeCaptureFlags {
		value, err := bridgeFlag(root, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if value == "1" {
			state[name] = 1
			change = append(change, name)
		}
	}
	if len(change) == 0 {
		return nil
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := writeDurableDNSMarker(bridgeStatePath(paths), body); err != nil {
		return err
	}
	for _, name := range change {
		if err := os.WriteFile(filepath.Join(root, name), []byte("0\n"), 0600); err != nil {
			return fmt.Errorf("prepare %s: %w", name, err)
		}
	}
	return nil
}

func restoreBridgeNetfilter(paths Paths, root string) error {
	state, err := readBridgeState(paths)
	if err != nil {
		return err
	}
	var result error
	for name := range state {
		value, err := bridgeFlag(root, name)
		if errors.Is(err, os.ErrNotExist) {
			delete(state, name)
			continue
		}
		if err == nil && value == "0" {
			err = os.WriteFile(filepath.Join(root, name), []byte("1\n"), 0600)
		}
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		delete(state, name)
	}
	if len(state) == 0 {
		if err := os.Remove(bridgeStatePath(paths)); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	} else {
		body, err := json.Marshal(state)
		if err == nil {
			err = writeDurableDNSMarker(bridgeStatePath(paths), body)
		}
		result = errors.Join(result, err)
	}
	return result
}
