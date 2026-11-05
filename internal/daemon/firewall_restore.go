package daemon

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

// Called by the renderer while resproxy-firewall holds its transaction lock.
// A queued netifd event must observe the phase after an earlier commit.
func RestoredCaptureMode(config settings.Config, paths Paths) (string, int, error) {
	if !config.Enabled || !config.RouteEnabled {
		return "", 0, errors.New("capture is disabled")
	}
	if ProtectionActive(paths) {
		return "guard", 0, nil
	}
	if CrashFallbackActive(paths) {
		return "emergency", 0, nil
	}
	body, err := os.ReadFile(dnsInteropPath(paths))
	if err != nil {
		return "", 0, err
	}
	var state DNSInterop
	if err := json.Unmarshal(body, &state); err != nil {
		return "", 0, err
	}
	switch state.EffectiveMode {
	case "system":
		return "apply", 0, nil
	case "proxy-only", "proxy-fallback-wan":
		return "apply", config.DNSPort, nil
	default:
		return "", 0, errors.New("invalid effective DNS mode")
	}
}
