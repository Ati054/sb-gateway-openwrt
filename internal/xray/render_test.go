package xray

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/subscription"
)

func TestRenderOutboundFromSubscription(t *testing.T) {
	feed := []byte("vless://2f1c08fc-3f43-4e95-8f57-f8bded750a1a@edge.example.test:443?security=tls&type=ws&path=%2Fproxy#CA%20Toronto\n" +
		"hysteria2://secret@hy.example.test:8443?sni=hy.example.test&obfs=salamander&obfs-password=obfs-secret#US%20Chicago")
	nodes, err := subscription.Parse("provider-a", feed, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		body, err := RenderOutbound(node)
		if err != nil {
			t.Fatal(err)
		}
		var outbound map[string]any
		if err := json.Unmarshal(body, &outbound); err != nil {
			t.Fatal(err)
		}
		settings := outbound["settings"].(map[string]any)
		if settings["address"] != node.Server || int(settings["port"].(float64)) != node.Port {
			t.Fatalf("outbound endpoint mismatch: %s", body)
		}
		if _, exists := outbound["tag"]; exists {
			t.Fatal("renderer must not fix the dynamic tag")
		}
		if node.Protocol == "vless" && settings["id"] != node.Credentials["uuid"] {
			t.Fatal("VLESS UUID was lost")
		}
		if node.Protocol == "hysteria2" {
			stream := outbound["streamSettings"].(map[string]any)
			mask := stream["finalmask"].(map[string]any)["udp"].([]any)[0].(map[string]any)
			if stream["hysteriaSettings"].(map[string]any)["auth"] != "secret" || mask["settings"].(map[string]any)["password"] != "obfs-secret" {
				t.Fatal("Hysteria 2 credentials were lost")
			}
		}
	}
}

func TestRenderOutboundRejectsMissingSecretsAndUnsupportedProtocols(t *testing.T) {
	node := catalog.Node{Protocol: "vless", Server: "edge.example", Port: 443}
	if _, err := RenderOutbound(node); err == nil {
		t.Fatal("accepted VLESS without UUID")
	}
	node.Protocol = "unsupported"
	node.Credentials = map[string]string{"password": "do-not-log"}
	if _, err := RenderOutbound(node); err == nil || strings.Contains(err.Error(), "do-not-log") {
		t.Fatalf("unsupported protocol exposed a secret: %v", err)
	}
	node.Protocol = "hysteria2"
	node.Settings = map[string]any{"obfs": map[string]any{"type": "salamander"}}
	if _, err := RenderOutbound(node); err == nil {
		t.Fatal("accepted Hysteria 2 obfuscation without password")
	}
}

func TestRenderOutboundAfterCatalogJSONRoundTrip(t *testing.T) {
	node := catalog.Node{Protocol: "hysteria2", Server: "hy.example", Port: 443,
		Settings:    map[string]any{"up_mbps": 25, "down_mbps": 100},
		Credentials: map[string]string{"password": "private"}}
	public, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	var restored catalog.Node
	if err := json.Unmarshal(public, &restored); err != nil {
		t.Fatal(err)
	}
	restored.Credentials = node.Credentials
	body, err := RenderOutbound(restored)
	if err != nil || !strings.Contains(string(body), `"protocol":"hysteria"`) {
		t.Fatalf("restored catalog cannot render: %s %v", body, err)
	}
}

func TestAdditionalProtocolTransportRendering(t *testing.T) {
	cases := []struct{ link, network, key, field, value string }{
		{"vmess://11111111-1111-1111-1111-111111111111@vm.example.test:443?type=ws&security=tls&sni=front.example.test&path=%2Fvm", "ws", "wsSettings", "path", "/vm"},
		{"trojan://private@tr.example.test:443?type=grpc&serviceName=tr&sni=front.example.test", "grpc", "grpcSettings", "serviceName", "tr"},
		{"trojan://private@tr.example.test:443?type=httpupgrade&path=%2Ftr", "httpupgrade", "httpupgradeSettings", "path", "/tr"},
		{"vmess://11111111-1111-1111-1111-111111111111@vm.example.test:443?type=xhttp&security=tls&path=%2Fvm&mode=auto", "xhttp", "xhttpSettings", "path", "/vm"},
	}
	for _, tt := range cases {
		t.Run(tt.network, func(t *testing.T) {
			nodes, err := subscription.Parse("transports", []byte(tt.link), 10)
			if err != nil {
				t.Fatal(err)
			}
			body, err := RenderOutbound(nodes[0])
			if err != nil {
				t.Fatal(err)
			}
			var outbound map[string]any
			json.Unmarshal(body, &outbound)
			stream := outbound["streamSettings"].(map[string]any)
			if stream["network"] != tt.network || stream["security"] != "tls" || stream[tt.key].(map[string]any)[tt.field] != tt.value {
				t.Fatal("TLS/transport settings were lost")
			}
		})
	}
}
