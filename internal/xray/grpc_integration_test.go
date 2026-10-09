package xray

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"example.org/resilient-proxy/internal/subscription"
	"github.com/xtls/xray-core/core"
)

// Black-box the importer/renderer and real gRPC transport against a controlled peer.
func TestImportedGRPCVLESSCarriesHTTP(t *testing.T) {
	uuid := "11111111-1111-1111-1111-111111111111"
	serverPort, proxyPort := freePort(t), freePort(t)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "grpc-traffic-verified") }))
	// The bundled core deliberately blackholes loopback destinations.
	// Use a local private interface for the controlled origin instead.
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var privateIP net.IP
	for _, address := range addresses {
		if network, ok := address.(*net.IPNet); ok && network.IP.To4() != nil && network.IP.IsPrivate() && !network.IP.IsLoopback() {
			privateIP = network.IP
			break
		}
	}
	if privateIP == nil {
		t.Skip("private non-loopback interface required for Xray freedom control")
	}
	origin.Listener, err = net.Listen("tcp", net.JoinHostPort(privateIP.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	origin.Start()
	defer origin.Close()
	serverConfig := map[string]any{
		"log":       map[string]any{"loglevel": "none"},
		"inbounds":  []any{map[string]any{"listen": "127.0.0.1", "port": serverPort, "protocol": "vless", "settings": map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": uuid}}}, "streamSettings": map[string]any{"network": "grpc", "grpcSettings": map[string]any{"serviceName": "sbg-release-grpc"}}}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []string{privateIP.String() + "/32"}}}}}},
	}
	serverJSON, _ := json.Marshal(serverConfig)
	server, err := core.StartInstance("json", serverJSON)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	uri := fmt.Sprintf("vless://%s@127.0.0.1:%d?encryption=none&security=none&type=grpc&mode=gun&serviceName=sbg-release-grpc#GRPC-control", uuid, serverPort)
	nodes, err := subscription.Parse("grpc-acceptance", []byte(uri), 1)
	if err != nil {
		t.Fatal(err)
	}
	outbound, err := RenderOutbound(nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	clientConfig := map[string]any{
		"log":       map[string]any{"loglevel": "none"},
		"inbounds":  []any{map[string]any{"listen": "127.0.0.1", "port": proxyPort, "protocol": "http", "settings": map[string]any{}}},
		"outbounds": []any{json.RawMessage(outbound)},
	}
	clientJSON, _ := json.Marshal(clientConfig)
	client, err := core.StartInstance("json", clientJSON)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DialContext: (&net.Dialer{Timeout: time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	if err != nil || response.StatusCode != 200 || strings.TrimSpace(string(body)) != "grpc-traffic-verified" {
		t.Fatalf("gRPC HTTP status=%d body-match=%t error=%v", response.StatusCode, string(body) == "grpc-traffic-verified", err)
	}
}
