package settings

import (
	"context"
	"errors"
	"testing"
)

func TestLANDeviceComesFromOpenWrt(t *testing.T) {
	for _, name := range []string{"br-lan", "br-home", "lan.22"} {
		config, err := ReadUCI(context.Background(), fake(map[string]string{
			"resproxy.main.route_enabled": "1", "network.lan.device": name,
			"resproxy.main.lan_interface": "ignored-old-device",
		}))
		if err != nil || config.LANInterface != name {
			t.Fatalf("LAN device %q: %+v %v", name, config, err)
		}
	}
}
func TestCaptureRequiresConfiguredLANDevice(t *testing.T) {
	for _, name := range []string{"", "br-lan;reboot", "@other", "device-name-too-long"} {
		if _, err := ReadUCI(context.Background(), fake(map[string]string{
			"resproxy.main.route_enabled": "1", "network.lan.device": name,
		})); err == nil {
			t.Fatalf("invalid LAN device %q accepted", name)
		}
	}
	_, err := ReadUCI(context.Background(), func(context.Context, string) (string, error) { return "", errors.New("missing") })
	if err != nil {
		t.Fatalf("disabled capture should not require LAN: %v", err)
	}
	if _, err := ReadUCI(context.Background(), fake(map[string]string{"resproxy.main.route_enabled": "1"})); err == nil {
		t.Fatal("missing LAN silently defaulted to br-lan")
	}
}
