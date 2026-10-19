// Package xray manages versioned outbound handlers in one long-lived Xray core.
// The command sequence is adapted from SB Gateway 1.6.21 xray_health.go.
package xray

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type CommandRunner func(context.Context, string, ...string) ([]byte, error)

// MutationMu serializes live switches and short routing snapshot/readback phases
// against probe selector updates. Independent probe lanes share its read side.
// Routing compilation runs outside this lock; the bundled core atomically
// carries the latest overrides into the replacement balancers.
var MutationMu sync.RWMutex

const maxDynamicOutboundsPerSelector = 10

type Manager struct {
	Binary          string
	Server          string
	TempDir         string
	Run             CommandRunner
	mu              sync.Mutex
	PrepareOutbound func(context.Context, map[string]any) error
	ReleaseOutbound func(string)
}

type SwitchResult struct {
	SelectedTag string
	PreviousTag string
	Changed     bool
}

func NewManager(binary, server, tempDir string) *Manager {
	return &Manager{Binary: binary, Server: server, TempDir: tempDir, Run: runCommand}
}

func runCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, binary, args...).CombinedOutput()
}

// Switch adds the new handler, selects it, and confirms the selector by
// readback. The caller retires PreviousTag only after committing its generation.
// Existing streams can continue using the old handler.
func (m *Manager) Switch(ctx context.Context, selector, nodeID string, definition json.RawMessage) (SwitchResult, error) {
	MutationMu.Lock()
	defer MutationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Binary == "" || m.Server == "" || m.Run == nil || selector == "" || nodeID == "" {
		return SwitchResult{}, errors.New("incomplete Xray selector configuration")
	}
	var outbound map[string]any
	if err := json.Unmarshal(definition, &outbound); err != nil || len(outbound) == 0 {
		return SwitchResult{}, errors.New("invalid Xray outbound definition")
	}
	tag := DynamicTag(selector, nodeID, definition)
	outbound["tag"] = tag
	if m.PrepareOutbound != nil {
		if err := m.PrepareOutbound(ctx, outbound); err != nil {
			return SwitchResult{}, err
		}
	} else if _, ok := outbound["_resproxy_plugin"]; ok {
		return SwitchResult{}, errors.New("plugin supervisor is unavailable")
	}
	previous, err := m.current(ctx, selector)
	if err != nil {
		return SwitchResult{}, err
	}
	if previous == tag {
		loaded, err := m.loaded(ctx, tag)
		if err != nil {
			return SwitchResult{}, err
		}
		if !loaded {
			outbound["tag"] = tag
			if err := m.add(ctx, outbound); err != nil {
				return SwitchResult{}, err
			}
			loaded, err = m.loaded(ctx, tag)
			if err != nil || !loaded {
				return SwitchResult{}, errors.New("selected Xray outbound was not restored")
			}
		}
		return SwitchResult{SelectedTag: tag, PreviousTag: previous}, nil
	}
	if err := m.admit(ctx, selector, tag); err != nil {
		return SwitchResult{}, err
	}
	outbound["tag"] = tag
	if err := m.add(ctx, outbound); err != nil {
		return SwitchResult{}, err
	}
	loaded, err := m.loaded(ctx, tag)
	if err != nil || !loaded {
		return SwitchResult{}, errors.New("new Xray outbound was not installed")
	}
	if err := m.override(ctx, selector, tag); err != nil {
		if !m.restoreIfPossible(ctx, selector, previous, tag) {
			return SwitchResult{}, fmt.Errorf("Xray selector state uncertain after failed override: %w", err)
		}
		return SwitchResult{}, err
	}
	actual, err := m.current(ctx, selector)
	if err != nil || actual != tag {
		if err == nil {
			err = errors.New("override mismatch")
		}
		if !m.restoreIfPossible(ctx, selector, previous, tag) {
			return SwitchResult{}, fmt.Errorf("Xray selector state uncertain after readback: got %q: %w", actual, err)
		}
		return SwitchResult{}, fmt.Errorf("Xray selector update not confirmed: got %q: %w", actual, err)
	}
	return SwitchResult{SelectedTag: tag, PreviousTag: previous, Changed: true}, nil
}

// Retire unregisters a handler after its replacement has been committed.
func (m *Manager) Retire(ctx context.Context, selector, tag string) error {
	MutationMu.Lock()
	defer MutationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if tag == "" {
		return nil
	}
	current, err := m.current(ctx, selector)
	if err != nil {
		return err
	}
	if current == tag {
		return errors.New("cannot retire selected Xray outbound")
	}
	loaded, err := m.loaded(ctx, tag)
	if err != nil {
		return err
	}
	if !loaded {
		if m.ReleaseOutbound != nil {
			m.ReleaseOutbound(tag)
		}
		return nil
	}
	_, err = m.command(ctx, "rmo", "--server="+m.Server, tag)
	if err != nil {
		// A concurrent health-controller switch may have retired the same tag.
		if loaded, checkErr := m.loaded(ctx, tag); checkErr == nil && !loaded {
			if m.ReleaseOutbound != nil {
				m.ReleaseOutbound(tag)
			}
			return nil
		}
		return errors.New("Xray outbound retirement was not confirmed")
	}
	if m.ReleaseOutbound != nil {
		m.ReleaseOutbound(tag)
	}
	return nil
}

func (m *Manager) loaded(ctx context.Context, tag string) (bool, error) {
	tags, err := m.inventory(ctx)
	if err != nil {
		return false, err
	}
	return tags[tag], nil
}

func (m *Manager) admit(ctx context.Context, selector, tag string) error {
	tags, err := m.inventory(ctx)
	if err != nil {
		return err
	}
	if tags[tag] {
		return nil
	}
	count := 0
	for existing := range tags {
		if strings.HasPrefix(existing, selector+"-") {
			count++
		}
	}
	if count >= maxDynamicOutboundsPerSelector {
		return errors.New("Xray dynamic outbound inventory limit is full")
	}
	return nil
}

func (m *Manager) inventory(ctx context.Context) (map[string]bool, error) {
	output, err := m.command(ctx, "lso", "--server="+m.Server)
	if err != nil {
		return nil, errors.New("Xray outbound inventory is unavailable")
	}
	var inventory struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(output, &inventory); err != nil || inventory.Outbounds == nil {
		return nil, errors.New("Xray outbound inventory is invalid")
	}
	tags := make(map[string]bool, len(inventory.Outbounds))
	for _, outbound := range inventory.Outbounds {
		tags[outbound.Tag] = true
	}
	return tags, nil
}

// Current reports the actual Xray override, rather than a cached intention.
func (m *Manager) Current(ctx context.Context, selector string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current(ctx, selector)
}

// CheckAPI probes the running core without waiting for a selector mutation.
// A subscription refresh may legitimately hold mu longer than the watchdog's
// deadline; that contention is not evidence that Xray has stopped responding.
func (m *Manager) CheckAPI(ctx context.Context) error {
	_, err := m.command(ctx, "statssys", "--server="+m.Server)
	return err
}

// Restore selects an already-present handler after a later persistence failure.
// It never removes the newly added handler; cleanup is a separate operation.
func (m *Manager) Restore(ctx context.Context, selector, tag string) error {
	MutationMu.Lock()
	defer MutationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if selector == "" || tag == "" {
		return errors.New("selector and handler tag are required")
	}
	if err := m.override(ctx, selector, tag); err != nil {
		return err
	}
	actual, err := m.current(ctx, selector)
	if err != nil || actual != tag {
		return errors.New("Xray selector restore was not confirmed")
	}
	return nil
}

func (m *Manager) current(ctx context.Context, selector string) (string, error) {
	output, err := m.command(ctx, "bi", "--server="+m.Server, selector)
	if err != nil {
		return "", errors.New("Xray selector state is unavailable")
	}
	section := false
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "Selecting Override:") {
			section = true
			continue
		}
		if section && strings.HasPrefix(line, "- Selects:") {
			break
		}
		if !section || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return fields[len(fields)-1], nil
		}
	}
	return "", nil
}

func (m *Manager) override(ctx context.Context, selector, tag string) error {
	if _, err := m.command(ctx, "bo", "--server="+m.Server, "-b", selector, tag); err != nil {
		return errors.New("Xray rejected selector override")
	}
	return nil
}

func (m *Manager) restoreIfPossible(ctx context.Context, selector, previous, attempted string) bool {
	if previous == "" {
		return false
	}
	if err := m.override(ctx, selector, previous); err != nil {
		return false
	}
	actual, err := m.current(ctx, selector)
	if err == nil && actual == previous {
		_, _ = m.command(ctx, "rmo", "--server="+m.Server, attempted)
		return true
	}
	return false
}

func (m *Manager) add(ctx context.Context, outbound map[string]any) error {
	body, err := json.Marshal(map[string]any{"outbounds": []any{outbound}})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(m.TempDir, "resproxyd-outbound-*.json")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	_, err = m.command(ctx, "ado", "--server="+m.Server, path)
	if err != nil {
		tag, ok := outbound["tag"].(string)
		if !ok || tag == "" {
			return errors.New("Xray rejected dynamic outbound")
		}
		loaded, inventoryErr := m.loaded(ctx, tag)
		if inventoryErr != nil || !loaded {
			return errors.New("Xray rejected dynamic outbound")
		}
	}
	return nil
}

func (m *Manager) command(ctx context.Context, action string, args ...string) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return m.Run(callCtx, m.Binary, append([]string{"api", action}, args...)...)
}

// DynamicTag changes only when the node's actual outbound definition changes.
func DynamicTag(selector, nodeID string, definition json.RawMessage) string {
	var value any
	if json.Unmarshal(definition, &value) == nil {
		definition, _ = json.Marshal(value)
	}
	// Match the health controller's runtime tag exactly. An unchanged active
	// outbound must not be replaced merely because the subscription refreshed.
	sum := sha256.Sum256(append([]byte(nodeID+"\x00"), definition...))
	return selector + "-" + hex.EncodeToString(sum[:])[:12]
}

func (m *Manager) Inventory(ctx context.Context) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inventory(ctx)
}
