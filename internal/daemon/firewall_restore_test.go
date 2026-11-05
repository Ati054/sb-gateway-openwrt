package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func TestRestoredCaptureModeReadsCurrentPhaseAndDNSLease(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	config := settings.Config{Enabled: true, RouteEnabled: true, DNSPort: 1053}
	if _, _, err := RestoredCaptureMode(config, paths); err == nil {
		t.Fatal("missing DNS state enabled capture")
	}
	if err := os.WriteFile(protectionFile(paths), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(crashFallbackFile(paths), nil, 0600); err != nil {
		t.Fatal(err)
	}
	check := func(mode string, port int) {
		t.Helper()
		got, dnsPort, err := RestoredCaptureMode(config, paths)
		if err != nil || got != mode || dnsPort != port {
			t.Fatalf("mode=%q port=%d err=%v; want %q/%d", got, dnsPort, err, mode, port)
		}
	}
	check("guard", 0)
	if err := os.Remove(protectionFile(paths)); err != nil {
		t.Fatal(err)
	}
	check("emergency", 0)
	if err := os.Remove(crashFallbackFile(paths)); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"proxy-only", "proxy-fallback-wan", "system"} {
		if err := publishDNSInterop(paths, DNSInterop{EffectiveMode: mode}); err != nil {
			t.Fatal(err)
		}
		port := 1053
		if mode == "system" {
			port = 0
		}
		check("apply", port)
	}
	if err := os.WriteFile(filepath.Join(paths.RunRoot, "dns-interop.json"), []byte(`{"effective_mode":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RestoredCaptureMode(config, paths); err == nil {
		t.Fatal("invalid DNS state enabled capture")
	}
}
