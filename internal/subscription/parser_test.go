package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"example.org/resilient-proxy/internal/catalog"
)

const testVLESS = "vless://2f1c08fc-3f43-4e95-8f57-f8bded750a1a@edge.example.test:443?security=tls&type=ws&path=%2Fproxy#CA%20Toronto"

func TestParseSupportedLinksAndKeepSecretsOutOfCatalogJSON(t *testing.T) {
	body := []byte(testVLESS + "\n" + "hysteria2://secret@hy.example.test:8443?sni=hy.example.test#US%20Chicago")
	nodes, err := Parse("provider-a", body, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].Protocol != "vless" || nodes[0].Transport != "ws" || nodes[0].Country != "CA" ||
		nodes[1].Protocol != "hysteria2" || nodes[1].Credentials["password"] != "secret" {
		t.Fatalf("unexpected parsed nodes: %+v", nodes)
	}
	encoded, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "2f1c08fc-3f43") {
		t.Fatalf("serialized catalog contains credentials: %s", encoded)
	}
	initial, _, err := catalog.Reconcile("provider-a", nil, nodes)
	if err != nil || len(initial) != 2 {
		t.Fatalf("parsed nodes cannot enter catalog: %v", err)
	}
}

func TestParseBase64AndRejectEmptyOrUnsupportedFeed(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(testVLESS))
	nodes, err := Parse("provider-a", []byte(encoded), 10)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("base64 feed: %v, %d nodes", err, len(nodes))
	}
	for _, body := range [][]byte{nil, []byte("trojan://unrelated"), []byte("vless://bad"), []byte{0xff}} {
		if _, err := Parse("provider-a", body, 10); err == nil {
			t.Fatalf("invalid feed accepted: %q", body)
		}
	}
}

func TestParseLimitDoesNotTruncateSilently(t *testing.T) {
	body := []byte(testVLESS + "\n" + testVLESS)
	if _, err := Parse("provider-a", body, 1); err == nil {
		t.Fatal("node limit silently truncated feed")
	}
}
