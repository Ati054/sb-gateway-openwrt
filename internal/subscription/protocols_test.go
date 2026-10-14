package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func vmessShare(raw string) string {
	return "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(raw))
}

func TestAdditionalProtocolFormsAndSecrets(t *testing.T) {
	cases := []struct{ link, protocol, secret string }{
		{vmessShare(`{"v":"2","ps":"DE Berlin","add":"vm.example.test","port":"443","id":"2f1c08fc-3f43-4e95-8f57-f8bded750a1a","aid":"0","net":"ws","host":"front.example.test","path":"/ws","tls":"tls","scy":"auto"}`), "vmess", "2f1c08fc-3f43-4e95-8f57-f8bded750a1a"},
		{vmessShare(`{"ps":"US New York","add":"vm.example.test","port":8080,"id":"2f1c08fc-3f43-4e95-8f57-f8bded750a1a","aid":0,"net":"tcp","type":"http","host":"front.example.test","path":"/","scy":"aes-128-gcm"}`), "vmess", "2f1c08fc-3f43-4e95-8f57-f8bded750a1a"},
		{"vmess://2f1c08fc-3f43-4e95-8f57-f8bded750a1a@vm.example.test:443?security=tls&type=grpc&serviceName=vm#DE%20Berlin", "vmess", "2f1c08fc-3f43-4e95-8f57-f8bded750a1a"},
		{"trojan://t%40p%3Ass-secret@tr.example.test?type=ws&path=%2Ft&sni=tr.example.test#CA%20Toronto", "trojan", "t@p:ss-secret"},
		{"ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:ss-private")) + "@[2001:db8::1]:8388#JP%20Tokyo", "shadowsocks", "ss-private"},
		{"ss://aes-128-gcm:ss%40private@ss.example.test:8388#US%20Seattle", "shadowsocks", "ss@private"},
		{"ss://" + base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:ss-private@ss.example.test:8388")) + "#US%20Seattle", "shadowsocks", "ss-private"},
		{"ss://2022-blake3-aes-128-gcm:" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")) + "@ss.example.test:8388#NL%20Amsterdam", "shadowsocks", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))},
	}
	for _, tt := range cases {
		t.Run(tt.protocol+tt.link[:6], func(t *testing.T) {
			nodes, err := Parse("source", []byte(tt.link), 10)
			if err != nil || len(nodes) != 1 {
				t.Fatalf("parse failed: %v", err)
			}
			n := nodes[0]
			if n.Protocol != tt.protocol || n.Country == "" || n.City == "" {
				t.Fatal("protocol/location metadata lost")
			}
			key := "password"
			if tt.protocol == "vmess" {
				key = "uuid"
			}
			if n.Credentials[key] != tt.secret {
				t.Fatal("credentials changed")
			}
			data, _ := json.Marshal(n)
			if strings.Contains(string(data), tt.secret) {
				t.Fatal("public catalog exposes credentials")
			}
		})
	}
}

func TestMixedProtocolsBase64AndPartialFeedValidation(t *testing.T) {
	valid := "trojan://password@tr.example.test:443#DE%20Berlin\nss://aes-256-gcm:password@ss.example.test:8388#CA%20Toronto\n" + vmessShare(`{"add":"vm.example.test","port":8080,"id":"2f1c08fc-3f43-4e95-8f57-f8bded750a1a","aid":0,"net":"tcp"}`)
	encoded := base64.StdEncoding.EncodeToString([]byte(testVLESS + "\n" + valid + "\nhy2://password@hy.example.test:443#US%20Chicago"))
	nodes, err := Parse("mixed", []byte(encoded), 10)
	if err != nil || len(nodes) != 5 {
		t.Fatalf("mixed feed: %d %v", len(nodes), err)
	}
	if nodes, stats, err := ParseWithStats("mixed", []byte(encoded), 4); err != nil || len(nodes) != 4 || stats.Limited != 1 {
		t.Fatal("limit did not preserve a bounded catalog and warning")
	}
	invalid := []string{
		"trojan://@tr.example.test:443",
		"trojan://do-not-expose@tr.example.test:443?allowInsecure=1",
		"ss://unsupported:do-not-expose@ss.example.test:8388",
		"ss://aes-256-gcm:do-not-expose@ss.example.test:8388/?plugin=unknown-plugin",
		"ss://2022-blake3-aes-128-gcm:badkey@ss.example.test:8388",
		vmessShare(`{"add":"vm.example.test","port":443,"id":"2f1c08fc-3f43-4e95-8f57-f8bded750a1a","aid":64}`),
	}
	for _, link := range invalid {
		if nodes, stats, err := ParseWithStats("mixed", []byte(testVLESS+"\n"+link), 10); err != nil || len(nodes) != 1 || stats.Invalid != 1 {
			t.Fatal("invalid node poisoned the valid catalog")
		}
		if _, err := Parse("mixed", []byte(link), 10); err == nil || strings.Contains(err.Error(), "do-not-expose") {
			t.Fatal("invalid-only feed accepted or secret exposed")
		}
	}
}

func TestSSPluginsAreImportedWithoutInstallationOrCredentialExposure(t *testing.T) {
	for _, plugin := range []string{"v2ray-plugin%3Bmode%3Dwebsocket%3Bhost%3Dfront.example.test%3Bpath%3D%2Fws", "obfs-local%3Bobfs%3Dtls%3Bobfs-host%3Dfront.example.test"} {
		nodes, err := Parse("source", []byte("ss://aes-256-gcm:private-password@ss.example.test:8388/?plugin="+plugin+"#DE%20Berlin"), 10)
		if err != nil || len(nodes) != 1 || nodes[0].Settings["plugin"] == nil {
			t.Fatalf("plugin import failed: %v", err)
		}
		body, _ := json.Marshal(nodes[0])
		if strings.Contains(string(body), "private-password") {
			t.Fatal("secret in public catalog")
		}
	}
}
