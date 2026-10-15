package agentport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Options contains only the fields used by the client health controller.
// RouterOS/container telemetry and service supervision are intentionally
// outside this OpenWrt port.
type Options struct {
	BackgroundBusy      func() bool
	OnProgress          func()
	XrayProcessName     string
	RunXrayCommand      func(context.Context, string, ...string) ([]byte, error)
	StateRoot           string
	XrayBinary          string
	XrayAPIServer       string
	XrayReadyFile       string
	HealthInterval      time.Duration
	HealthPoolFile      string
	DNSGenerationFile   string
	HotRuntimeReadyFile string
	XrayFailureSocket   string
	PassiveSocket       string
	ProbeURL            string
	ProbeBasePort       int
	PrepareOutbound     func(context.Context, map[string]any) error
	ReleaseOutbound     func(string)
}

func (opts Options) xrayProcessName() string {
	if opts.XrayProcessName != "" {
		return opts.XrayProcessName
	}
	return "xray"
}

func envOr(_ string, fallback string) string { return fallback }

func wait(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func statePath(root, name string) string { return filepath.Join(root, name+".json") }

func readJSON(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return errors.New("invalid health JSON file or size")
	}
	decoder := json.NewDecoder(io.LimitReader(file, (32<<20)+1))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("health JSON has trailing or oversized data")
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	return writeJSONAtomicContext(context.Background(), path, value)
}

func writeJSONAtomicContext(ctx context.Context, path string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if previous, err := os.ReadFile(path); err == nil && bytes.Equal(previous, body) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".health-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(temporary, path)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
