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
	if got := externalDNSServiceAt(dir, func(string) bool { return false }); got != "" {
		t.Fatalf("stopped but installed DNS service blocked managed DNS: %s", got)
	}
	if got := externalDNSServiceAt(dir, func(candidate string) bool { return candidate == path }); got != "https-dns-proxy" {
		t.Fatalf("running DNS service not detected: %s", got)
	}
}

func TestDNSInteropPreservesExternalDNS(t *testing.T) {
	const proxy = "proxy-fallback-wan"
	cases := []struct {
		name    string
		rules   string
		err     error
		service string
		want    string
		reason  string
	}{
		{"no existing redirect", `table inet fw4 { chain input { tcp dport 22 accept } }`, nil, "", proxy, ""},
		{"https-dns-proxy owns DNS with forced LAN", "", nil, "https-dns-proxy", "system", "https-dns-proxy-running"},
		{"https-dns-proxy owns router DNS without forced LAN", "", nil, "https-dns-proxy", "system", "https-dns-proxy-running"},
		{"smartdns owns DNS upstream", "", nil, "smartdns", "system", "other-dns-service"},
		{"uninstalled service leaves no conflict", "", nil, "", proxy, ""},
		{"other DNS redirect", "table inet fw4 {\n chain dstnat_lan {\n tcp dport { 53, 853 } redirect to :53\n }\n}", nil, "", "system", "other-dns-redirect"},
		{"cannot inspect firewall", "", errors.New("nft unavailable"), "", "system", "firewall-inspection-failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveDNSInterop(context.Background(), proxy, func(context.Context) (string, error) {
				return tc.rules, tc.err
			}, func() string { return tc.service })
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

func TestDNSInteropIgnoresOwnStaleRulesButFindsOtherRedirect(t *testing.T) {
	rules := `table ip resproxy {
  chain dns_prerouting {
    udp dport 53 redirect to :1053
  }
}
table ip6 resproxy6 {
  chain dns_prerouting {
    tcp dport 53 redirect to :1053
  }
}`
	if hasCompetingDNSRedirect(rules) {
		t.Fatal("own resproxy rules were classified as a competing service")
	}
	if !hasCompetingDNSRedirect(rules + "\ntable inet other {\n chain prerouting {\n udp dport 53 tproxy to :5300\n }\n}") {
		t.Fatal("independent DNS interceptor was missed")
	}
	if hasCompetingDNSRedirect("table inet fw4 {\n tcp dport 853 redirect to :853\n}") {
		t.Fatal("DoT-only rule was confused with DNS port 53")
	}
}
