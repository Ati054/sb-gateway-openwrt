package settings

import (
	"context"
	"testing"
)

func TestManagedDNSModePropagatesToCoreAndCapture(t *testing.T) {
	for _, mode := range []string{"system", "proxy-only", "proxy-fallback-wan"} {
		t.Run(mode, func(t *testing.T) {
			c, err := ReadUCI(context.Background(), fake(map[string]string{"resproxy.main.dns_mode": mode, "resproxy.main.dns_port": "1153"}))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "system" {
				if c.Base.ManagedDNSPort != 0 || c.Capture.ProtocolDNS {
					t.Fatal("system DNS enabled managed capture")
				}
			} else if c.Base.ManagedDNSPort != 1153 || !c.Capture.ProtocolDNS {
				t.Fatal("managed DNS backend and packet capture disagree")
			}
		})
	}
}
