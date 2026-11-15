package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalDNSServiceOnlyBlocksWhenRunning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "https-dns-proxy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := externalDNSServiceAt(dir, func(string) (bool, error) { return false, nil }); got != "" || err != nil {
		t.Fatalf("stopped but installed DNS service blocked managed DNS: %s", got)
	}
	if got, err := externalDNSServiceAt(dir, func(candidate string) (bool, error) { return candidate == path, nil }); got != "https-dns-proxy" || err != nil {
		t.Fatalf("running DNS service not detected: %s", got)
	}
}

func TestDNSInteropPreservesExternalDNS(t *testing.T) {
	const proxy = "proxy-fallback-wan"
	cases := []struct {
		name     string
		conflict bool
		err      error
		service  string
		want     string
		reason   string
	}{
		{"no existing redirect", false, nil, "", proxy, ""},
		{"https-dns-proxy owns DNS with forced LAN", false, nil, "https-dns-proxy", "system", "https-dns-proxy-running"},
		{"https-dns-proxy owns router DNS without forced LAN", false, nil, "https-dns-proxy", "system", "https-dns-proxy-running"},
		{"smartdns owns DNS upstream", false, nil, "smartdns", "system", "other-dns-service"},
		{"uninstalled service leaves no conflict", false, nil, "", proxy, ""},
		{"other DNS redirect", true, nil, "", "system", "other-dns-redirect"},
		{"cannot inspect firewall", false, errors.New("nft unavailable"), "", "system", "firewall-inspection-failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveDNSInterop(context.Background(), proxy, func(context.Context) (bool, error) {
				return tc.conflict, tc.err
			}, func(context.Context) (string, error) { return tc.service, nil })
			if got.RequestedMode != proxy || got.EffectiveMode != tc.want || got.Reason != tc.reason || got.ConflictingService != tc.service {
				t.Fatalf("got %+v, want mode=%s reason=%s", got, tc.want, tc.reason)
			}
		})
	}
}

func TestDNSInteropDoesNotInspectWhenExistingDNSSelected(t *testing.T) {
	got := resolveDNSInterop(context.Background(), "system", nil, nil)
	if got.EffectiveMode != "system" || got.Reason != "" {
		t.Fatalf("existing DNS changed: %+v", got)
	}
}

func TestDNSInteropOperationalFailureDoesNotPublishFullReadiness(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	for _, reason := range []string{"firewall-inspection-failed", "dns-service-inspection-failed", "listener-failed", "router-dns-handoff-failed", "wan-dns-preflight-failed"} {
		state := DNSInterop{RequestedMode: "proxy-only", EffectiveMode: "system", Reason: reason}
		if dnsInteropReady(state) || filepath.Base(hotRuntimeReadyPath(paths, state)) == "hot-ready.json" {
			t.Errorf("operational DNS failure reported as fully ready: %s", reason)
		}
	}
	for _, state := range []DNSInterop{
		{RequestedMode: "system", EffectiveMode: "system"},
		{RequestedMode: "proxy-only", EffectiveMode: "off"},
		{RequestedMode: "proxy-only", EffectiveMode: "proxy-only"},
		{RequestedMode: "proxy-only", EffectiveMode: "system", Reason: "https-dns-proxy-running"},
		{RequestedMode: "proxy-only", EffectiveMode: "system", Reason: "other-dns-service"},
		{RequestedMode: "proxy-only", EffectiveMode: "system", Reason: "other-dns-redirect"},
	} {
		if !dnsInteropReady(state) || filepath.Base(hotRuntimeReadyPath(paths, state)) != "hot-ready.json" {
			t.Errorf("supported DNS ownership blocked readiness: %+v", state)
		}
	}
}
