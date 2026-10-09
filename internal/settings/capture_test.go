package settings

import (
	"context"
	"testing"
)

func TestCaptureUCI(t *testing.T) {
	config, err := ReadUCI(context.Background(), fake(map[string]string{
		"resproxy.main.ipv6_mode": "block", "resproxy.main.bypass_cidrs": "203.0.113.4 2001:db8:1::12/64"}))
	if err != nil || config.Capture.IPv6Mode != "block" || len(config.Capture.DirectIPs) != 2 || config.Capture.DirectIPs[1] != "2001:db8:1::/64" {
		t.Fatalf("%+v %v", config.Capture, err)
	}
	for key, value := range map[string]string{"ipv6_mode": "bad", "bypass_cidrs": "192.168.1.400"} {
		if _, err := ReadUCI(context.Background(), fake(map[string]string{"resproxy.main." + key: value})); err == nil {
			t.Fatal("invalid capture setting accepted")
		}
	}
}
