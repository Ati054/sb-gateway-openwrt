package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDNSServiceInspectionHonorsCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenWrt init scripts require a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "https-dns-proxy"), []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	at := time.Now()
	service, err := inspectExternalDNSServiceAt(ctx, dir)
	if service != "https-dns-proxy" || !errors.Is(err, context.DeadlineExceeded) || time.Since(at) > 2*time.Second {
		t.Fatalf("hung service check not bounded: service=%q err=%v elapsed=%v", service, err, time.Since(at))
	}
}

func TestDNSServiceInspectionSeparatesStoppedFromUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenWrt init scripts require a POSIX shell")
	}
	for _, status := range []struct {
		exit   int
		name   string
		failed bool
	}{{0, "https-dns-proxy", false}, {1, "", false}, {2, "https-dns-proxy", true}} {
		dir := t.TempDir()
		body := []byte("#!/bin/sh\nexit " + string(rune('0'+status.exit)) + "\n")
		if err := os.WriteFile(filepath.Join(dir, "https-dns-proxy"), body, 0700); err != nil {
			t.Fatal(err)
		}
		name, err := inspectExternalDNSServiceAt(t.Context(), dir)
		if name != status.name || (err != nil) != status.failed {
			t.Fatalf("exit %d: name=%q err=%v", status.exit, name, err)
		}
	}
}

func TestDNSServiceInspectionFailurePreservesExistingDNS(t *testing.T) {
	called := false
	state := resolveDNSInterop(t.Context(), "proxy-only", func(context.Context) (bool, error) { called = true; return false, nil }, func(context.Context) (string, error) { return "https-dns-proxy", context.DeadlineExceeded })
	if called || state.EffectiveMode != "system" || state.Reason != "dns-service-inspection-failed" || state.ConflictingService != "https-dns-proxy" {
		t.Fatalf("uncertain external DNS ownership was overridden: %+v", state)
	}
}
