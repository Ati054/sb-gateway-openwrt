package sip003

import (
	"strings"
	"testing"
)

func TestWhitelistedSIP003Options(t *testing.T) {
	for _, value := range []string{"v2ray-plugin", "v2ray-plugin;tls;mode=websocket;host=front.example;path=/ws;", `v2ray-plugin;path=/ws?a\=b\;c`, `obfs-local;obfs=tls;obfs-host=front.example`, `simple-obfs;obfs=http;obfs-uri=/test`} {
		cfg, err := Parse(value)
		if err != nil {
			t.Fatalf("%s: %v", value, err)
		}
		again, err := Parse(cfg.Name + ";" + cfg.Options)
		if err != nil || again != cfg {
			t.Fatalf("normalization changed: %+v %+v %v", cfg, again, err)
		}
	}
	for _, value := range []string{"/tmp/v2ray-plugin", "v2ray-plugin;server", "v2ray-plugin;cert=/tmp/cert", "v2ray-plugin;key=secret", "v2ray-plugin;host=front\n", "v2ray-plugin;mode=bad", "v2ray-plugin;tls=0", "v2ray-plugin;host=one;host=two", "obfs-local", "obfs-local;obfs=bad", "obfs-local;failover=somewhere", strings.Repeat("x", 2049)} {
		if _, err := Parse(value); err == nil {
			t.Fatalf("unsafe options accepted: %q", value)
		}
	}
}
