package sip003

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Metadata = "_resproxy_plugin"

type helper struct {
	config Config
	host   string
	remote int
	port   int
	cmd    *exec.Cmd
	done   chan error
	owners map[string]bool
	last   time.Time
}
type Manager struct {
	mu         sync.Mutex
	entries    map[string]*helper
	tags       map[string]string
	BinaryPath func(string) string
	closed     bool
}

func NewManager() *Manager {
	return &Manager{entries: map[string]*helper{}, tags: map[string]string{}, BinaryPath: Binary}
}

// Prepare is called at use time, never while importing an entire subscription.
// Stable loopback ports allow an exited helper to restart without replacing Xray.
func (m *Manager) Prepare(ctx context.Context, out map[string]any) error {
	raw, exists := out[Metadata]
	if !exists {
		return nil
	}
	body, _ := json.Marshal(raw)
	var cfg Config
	if json.Unmarshal(body, &cfg) != nil {
		return errors.New("invalid plugin configuration")
	}
	validated, err := Parse(cfg.Name + ";" + cfg.Options)
	if err != nil {
		return err
	}
	cfg = validated
	settings, ok := out["settings"].(map[string]any)
	if !ok || out["protocol"] != "shadowsocks" {
		return errors.New("invalid plugin outbound")
	}
	host, _ := settings["address"].(string)
	port := 0
	switch v := settings["port"].(type) {
	case int:
		port = v
	case float64:
		port = int(v)
	}
	tag, _ := out["tag"].(string)
	if host == "" || port < 1 || port > 65535 || tag == "" {
		return errors.New("invalid plugin endpoint")
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(cfg.Name+"\x00"+cfg.Options+"\x00"+net.JoinHostPort(host, strconv.Itoa(port)))))
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("plugin supervisor is stopped")
	}
	h := m.entries[key]
	if h == nil {
		if len(m.entries) >= 256 {
			m.pruneMetadata()
		}
		if len(m.entries) >= 256 {
			return errors.New("plugin inventory limit reached")
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return errors.New("plugin port unavailable")
		}
		local := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		h = &helper{config: cfg, host: host, remote: port, port: local, owners: map[string]bool{}}
		m.entries[key] = h
	}
	m.observe(h)
	if h.cmd == nil {
		running := 0
		for _, existing := range m.entries {
			m.observe(existing)
			if existing.cmd != nil && len(existing.owners) == 0 && !hasConnections(existing.port) {
				m.stop(existing)
			}
			if existing.cmd != nil {
				running++
			}
		}
		if running >= 4 {
			return errors.New("plugin process limit reached")
		}
		if err := m.start(ctx, h); err != nil {
			return err
		}
	}
	h.owners[tag] = true
	h.last = time.Now()
	m.tags[tag] = key
	delete(out, Metadata)
	settings["address"] = "127.0.0.1"
	settings["port"] = h.port
	// SIP003 forwards TCP only. UDP reaches no listening socket and fails closed;
	// it must never bypass the plugin by contacting the remote SS server directly.
	return nil
}
func (m *Manager) start(ctx context.Context, h *helper) error {
	binary := m.BinaryPath(h.config.Name)
	if binary == "" {
		return errors.New("unknown plugin")
	}
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "SS_REMOTE_HOST="+h.host, "SS_REMOTE_PORT="+strconv.Itoa(h.remote), "SS_LOCAL_HOST=127.0.0.1", "SS_LOCAL_PORT="+strconv.Itoa(h.port), "SS_PLUGIN_OPTIONS="+h.config.Options, "GOMEMLIMIT=16MiB", "GOGC=50")
	configureChild(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("install %s to use this node", h.config.Name)
	}
	h.cmd = cmd
	h.done = make(chan error, 1)
	done := h.done
	go func() { done <- cmd.Wait() }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stop(h)
			return ctx.Err()
		case <-deadline.C:
			m.stop(h)
			return errors.New("plugin did not open its local TCP port")
		case <-h.done:
			h.cmd = nil
			return errors.New("plugin failed to start")
		case <-tick.C:
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(h.port)), 30*time.Millisecond)
			if err == nil {
				conn.Close()
				return nil
			}
		}
	}
}
func (m *Manager) observe(h *helper) {
	if h.cmd != nil {
		select {
		case <-h.done:
			h.cmd = nil
		default:
		}
	}
}
func (m *Manager) stop(h *helper) {
	if h.cmd == nil {
		return
	}
	_ = h.cmd.Process.Kill()
	<-h.done
	h.cmd = nil
}
func (m *Manager) Release(tag string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if key := m.tags[tag]; key != "" {
		delete(m.entries[key].owners, tag)
		delete(m.tags, tag)
	}
}

// Reconciliation also cleans up handlers whose CLI acknowledgement was lost.
func (m *Manager) Sweep(ctx context.Context, loaded map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if loaded != nil {
		for tag, key := range m.tags {
			if !loaded[tag] {
				delete(m.entries[key].owners, tag)
				delete(m.tags, tag)
			}
		}
	}
	for _, h := range m.entries {
		m.observe(h)
		if len(h.owners) == 0 {
			if time.Since(h.last) > 30*time.Second && !hasConnections(h.port) {
				m.stop(h)
			}
			continue
		}
		if h.cmd == nil {
			_ = m.start(ctx, h)
		}
	}
}
func (m *Manager) pruneMetadata() {
	for key, h := range m.entries {
		if len(h.owners) == 0 && time.Since(h.last) > time.Minute && !hasConnections(h.port) {
			m.stop(h)
			delete(m.entries, key)
		}
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, h := range m.entries {
		m.stop(h)
	}
}

// Retiring an Xray handler does not end its existing streams. Keep the helper
// while any accepted TCP connection still uses its loopback port.
func hasConnections(port int) bool {
	file, err := os.Open("/proc/net/tcp")
	if err != nil {
		return false
	}
	defer file.Close()
	body, _ := io.ReadAll(io.LimitReader(file, 1<<20))
	suffix := fmt.Sprintf(":%04X", port)
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 3 && fields[3] == "01" && strings.HasSuffix(fields[1], suffix) {
			return true
		}
	}
	return false
}

func (m *Manager) HasEntries() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries) > 0 && !m.closed
}
