package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestStructuredFormatsImportConnectionsOnly(t *testing.T) {
	fixtures := []string{
		`proxies:
  - name: DE Berlin
    type: vless
    server: edge.example.test
    port: 443
    uuid: 2f1c08fc-3f43-4e95-8f57-f8bded750a1a
    tls: true
    network: ws
    ws-opts:
      path: /proxy
      headers:
        Host: front.example.test
  - name: unsupported
    type: ssh
    server: ignored.example.test
rules:
 - MATCH,DIRECT
`,
		`{"outbounds":[{"type":"vless","tag":"DE Berlin","server":"edge.example.test","server_port":443,"uuid":"2f1c08fc-3f43-4e95-8f57-f8bded750a1a","tls":{"enabled":true},"transport":{"type":"ws","path":"/proxy","headers":{"Host":"front.example.test"}}},{"type":"direct"}],"inbounds":[{"listen":"0.0.0.0","port":1}],"route":{"final":"direct"}}`,
		`{"outbounds":[{"tag":"DE Berlin","protocol":"vless","settings":{"vnext":[{"address":"edge.example.test","port":443,"users":[{"id":"2f1c08fc-3f43-4e95-8f57-f8bded750a1a"}]}]},"streamSettings":{"network":"ws","security":"tls","tlsSettings":{"serverName":"edge.example.test"},"wsSettings":{"path":"/proxy","headers":{"Host":"front.example.test"}}}},{"protocol":"freedom"}],"routing":{"domainStrategy":"AsIs"}}`,
	}
	for i, body := range fixtures {
		nodes, stats, err := ParseWithStats("structured", []byte(body), 10)
		if err != nil || len(nodes) != 1 || stats.Unsupported != 1 {
			t.Fatalf("format %d: n=%d stats=%+v err=%v", i, len(nodes), stats, err)
		}
		if nodes[0].Protocol != "vless" || nodes[0].Transport != "ws" || nodes[0].Credentials["uuid"] != "2f1c08fc-3f43-4e95-8f57-f8bded750a1a" {
			t.Fatalf("format %d lost connection", i)
		}
		encoded, _ := json.Marshal(nodes)
		if strings.Contains(string(encoded), "2f1c08fc") {
			t.Fatal("credential in public catalog")
		}
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(fixtures[1]))
	if nodes, err := Parse("encoded-json", []byte(encoded), 10); err != nil || len(nodes) != 1 {
		t.Fatal("base64 JSON unsupported", err)
	}
}
func TestStructuredUnsafeEntryIsSkippedAndLimitIsReported(t *testing.T) {
	body := `{"outbounds":[{"type":"trojan","server":"bad.example.test","server_port":443,"password":"do-not-expose","tls":{"enabled":true,"insecure":true}},{"type":"trojan","server":"good.example.test","server_port":443,"password":"secret","tls":{"enabled":true}},{"type":"trojan","server":"next.example.test","server_port":443,"password":"secret","tls":{"enabled":true}}]}`
	nodes, stats, err := ParseWithStats("source", []byte(body), 1)
	if err != nil || len(nodes) != 1 || nodes[0].Server != "good.example.test" || stats.Invalid != 1 || stats.Limited != 1 {
		t.Fatalf("unsafe import/limit: %d %+v %v", len(nodes), stats, err)
	}
	for _, invalid := range []string{`{"outbounds":[{"type":"trojan","server":"a.example.test","server_port":443,"password":"do-not-expose","tls":{"enabled":true,"insecure":true}}]}`, `{"outbounds":[`, `{"outbounds":[]} {}`, `{"route":{}}`} {
		if _, err := Parse("source", []byte(invalid), 10); err == nil || strings.Contains(err.Error(), "do-not-expose") {
			t.Fatal("bad structured feed accepted or secret exposed")
		}
	}
}

func TestXrayCompactSettingsAndMixedEndpoints(t *testing.T) {
	body := `{"outbounds":[{"protocol":"vless","settings":{"address":"compact.example.test","port":443,"id":"2f1c08fc-3f43-4e95-8f57-f8bded750a1a","encryption":"none"},"streamSettings":{"security":"tls"}},{"protocol":"trojan","settings":{"servers":[{"address":"","port":443,"password":"secret"},{"address":"good.example.test","port":443,"password":"secret"}]},"streamSettings":{"security":"tls"}},{"protocol":"hysteria","settings":{"version":2,"address":"hy.example.test","port":443},"streamSettings":{"security":"tls","network":"hysteria","hysteriaSettings":{"version":2,"auth":"secret"}}}]}`
	nodes, stats, err := ParseWithStats("source", []byte(body), 10)
	if err != nil || len(nodes) != 3 || stats.Invalid != 1 || nodes[2].Protocol != "hysteria2" {
		t.Fatal("compact or mixed import failed", len(nodes), stats, err)
	}
}
func TestYAMLAliasExpansionRejected(t *testing.T) {
	body := "a: &a [1,1,1,1,1,1,1,1,1]\n"
	previous := "a"
	for _, name := range []string{"b", "c", "d", "e", "f", "g", "h"} {
		body += name + ": &" + name + " [" + strings.TrimSuffix(strings.Repeat("*"+previous+",", 9), ",") + "]\n"
		previous = name
	}
	body += "proxies: *" + previous + "\n"
	if _, err := Parse("source", []byte(body), 10); err == nil {
		t.Fatal("alias expansion accepted")
	}
}
