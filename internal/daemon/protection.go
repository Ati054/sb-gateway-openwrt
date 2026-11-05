package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func protectionFile(paths Paths) string {
	return filepath.Join(paths.RunRoot, "capture-protected.active")
}
func ProtectionActive(paths Paths) bool { _, err := os.Stat(protectionFile(paths)); return err == nil }

func ProtectCapture(ctx context.Context, config settings.Config, paths Paths) error {
	if !config.Enabled || !config.RouteEnabled {
		return nil
	}
	// The script publishes the marker before releasing the firewall lock.
	return setFirewall(ctx, "guard", config.LANInterface, config.Base.TransparentPort, 0, config.Capture)
}
func ClearProtection(paths Paths) error {
	err := os.Remove(protectionFile(paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
