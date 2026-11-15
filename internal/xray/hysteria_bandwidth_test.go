package xray

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ati054/xroute-openwrt/internal/catalog"
	"github.com/Ati054/xroute-openwrt/internal/subscription"
	"github.com/xtls/xray-core/infra/conf"
)

func TestHysteriaBandwidthSurvivesImportCatalogAndXrayBuild(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		up, down     uint64
		obfs         bool
	}{
		{"both", "&upmbps=25&downmbps=100", 25_000_000 / 8, 100_000_000 / 8, false},
		{"upload-only", "&upmbps=17", 17_000_000 / 8, 0, false},
		{"download-with-obfs", "&downmbps=333&obfs=salamander&obfs-password=local-mask", 0, 333_000_000 / 8, true},
		{"automatic", "", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feed := "hysteria2://local-control@hy.example.test:443?sni=hy.example.test" + tc.params
			nodes, err := subscription.Parse("local", []byte(feed), 1)
			if err != nil || len(nodes) != 1 {
				t.Fatalf("import: %v", err)
			}
			stored, err := json.Marshal(nodes[0])
			if err != nil {
				t.Fatal(err)
			}
			var node catalog.Node
			if err := json.Unmarshal(stored, &node); err != nil {
				t.Fatal(err)
			}
			node.Credentials = nodes[0].Credentials
			outbound, err := RenderOutbound(node)
			if err != nil {
				t.Fatal(err)
			}
			var rendered struct {
				Stream json.RawMessage `json:"streamSettings"`
			}
			if err := json.Unmarshal(outbound, &rendered); err != nil {
				t.Fatal(err)
			}
			var stream conf.StreamConfig
			if err := json.Unmarshal(rendered.Stream, &stream); err != nil {
				t.Fatal(err)
			}
			built, err := stream.Build()
			if err != nil {
				t.Fatal(err)
			}
			if tc.up == 0 && tc.down == 0 {
				if built.QuicParams != nil {
					t.Fatal("automatic mode gained forced bandwidth settings")
				}
			} else if built.QuicParams == nil || built.QuicParams.BrutalUp != tc.up || built.QuicParams.BrutalDown != tc.down {
				t.Fatalf("lost bandwidth or wrong Mbps conversion: got %+v, want up=%d down=%d bytes/s", built.QuicParams, tc.up, tc.down)
			}
			if tc.obfs && len(built.Udpmasks) != 1 {
				t.Fatal("QUIC parameters replaced the provisioned obfuscation")
			}
			if built.QuicParams != nil && (built.QuicParams.InitStreamReceiveWindow != 0 || built.QuicParams.MaxStreamReceiveWindow != 0 || built.QuicParams.Congestion != "") {
				t.Fatal("bandwidth import changed flow-control windows or forced a congestion mode")
			}
		})
	}
}

func TestHysteriaBandwidthBoundsAndExplicitZero(t *testing.T) {
	for _, settings := range []map[string]any{
		{"up_mbps": -1}, {"down_mbps": 1.5}, {"up_mbps": 9_007_199_255.0},
	} {
		node := catalog.Node{Protocol: "hysteria2", Server: "hy.example.test", Port: 443, Settings: settings, Credentials: map[string]string{"password": "PRIVATE-CANARY"}}
		if _, err := RenderOutbound(node); err == nil || strings.Contains(err.Error(), "PRIVATE-CANARY") {
			t.Fatal("invalid bandwidth accepted or exposed credentials")
		}
	}
	node := catalog.Node{Protocol: "hysteria2", Server: "hy.example.test", Port: 443, Settings: map[string]any{"up_mbps": 0, "down_mbps": 0}, Credentials: map[string]string{"password": "local-control"}}
	body, err := RenderOutbound(node)
	if err != nil || strings.Contains(string(body), "quicParams") {
		t.Fatal("explicit zero must retain automatic bandwidth")
	}
}
