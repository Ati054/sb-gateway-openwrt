package xray

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"example.org/resilient-proxy/internal/subscription"
)

func testBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("RESPROXY_TEST_XRAY")
	if binary == "" {
		t.Skip("set RESPROXY_TEST_XRAY for real Xray integration tests")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatal(err)
	}
	return binary
}

var integrationPorts = struct {
	sync.Mutex
	used map[int]bool
}{used: map[int]bool{}}

func freePort(t *testing.T) int {
	t.Helper()
	integrationPorts.Lock()
	defer integrationPorts.Unlock()
	for {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		if !integrationPorts.used[port] {
			integrationPorts.used[port] = true
			return port
		}
	}
}

func testBase(t *testing.T) []byte {
	t.Helper()
	config := DefaultBaseConfig()
	config.APIPort = freePort(t)
	config.TransparentPort = freePort(t)
	config.HTTPPort = freePort(t)
	config.SocksPort = freePort(t)
	config.ProbeBasePort = 23000
	for config.ProbeBasePort == config.APIPort || config.ProbeBasePort == config.TransparentPort || config.ProbeBasePort == config.HTTPPort {
		config.ProbeBasePort += 10
	}
	body, err := RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func runTest(t *testing.T, binary string, body []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xray.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(binary, "run", "-test", "-c", path).CombinedOutput()
	if err != nil {
		t.Fatalf("Xray rejected config: %v\n%s", err, output)
	}
}

func TestRealXrayAcceptsBaseAndOutbounds(t *testing.T) {
	binary := testBinary(t)
	body := testBase(t)
	runTest(t, binary, body)
	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	nodes, err := subscription.Parse("test", []byte("vless://11111111-1111-1111-1111-111111111111@example.com:443?security=tls&type=ws&path=%2F#CA%20Toronto\n"+
		"hysteria2://test@example.com:443?sni=example.com#US%20Chicago\n"+
		"vmess://11111111-1111-1111-1111-111111111111@example.com:443?type=ws&security=tls&path=%2Fvm#DE%20Berlin\n"+
		"trojan://private@example.com:443?type=grpc&serviceName=tr#NL%20Amsterdam\n"+
		"ss://aes-256-gcm:private@example.com:8388#JP%20Tokyo"), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		outbound, err := RenderOutbound(node)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(outbound, &value); err != nil {
			t.Fatal(err)
		}
		value["tag"] = node.ID
		config["outbounds"] = append(config["outbounds"].([]any), value)
	}
	body, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	runTest(t, binary, body)
}

func TestRealXrayAcceptsProxiedRoutingDNS(t *testing.T) {
	binary := testBinary(t)
	config := DefaultBaseConfig()
	config.DomainStrategy = "IPOnDemand"
	body, err := RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	runTest(t, binary, body)
}

func TestRealXrayDynamicSwitchKeepsPID(t *testing.T) {
	binary := testBinary(t)
	base := testBase(t)
	var config struct {
		Inbounds []struct {
			Tag  string `json:"tag"`
			Port int    `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(base, &config); err != nil {
		t.Fatal(err)
	}
	apiPort := 0
	httpPort := 0
	for _, inbound := range config.Inbounds {
		if inbound.Tag == "resproxy-api" {
			apiPort = inbound.Port
		} else if inbound.Tag == "resproxy-http" {
			httpPort = inbound.Port
		}
	}
	path := filepath.Join(t.TempDir(), "base.json")
	if err := os.WriteFile(path, base, 0o600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(binary, "run", "-c", path)
	var output strings.Builder
	process.Stdout, process.Stderr = &output, &output
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Process.Kill(); _ = process.Wait() }()
	manager := NewManager(binary, fmt.Sprintf("127.0.0.1:%d", apiPort), t.TempDir())
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := manager.Current(ctx, "resproxy-main")
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("API unavailable: %v\n%s", err, output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	pid := process.Process.Pid
	var replacedTag string
	streamGate := make(chan struct{})
	streamTarget := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "before\n")
		writer.(http.Flusher).Flush()
		select {
		case <-streamGate:
			_, _ = io.WriteString(writer, "after\n")
		case <-request.Context().Done():
		}
	}))
	var localIP net.IP
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if ipNet, ok := address.(*net.IPNet); ok && ipNet.IP.To4() != nil && ipNet.IP.IsPrivate() {
			localIP = ipNet.IP
			break
		}
	}
	if localIP == nil {
		t.Skip("non-loopback local address required for HTTP proxy stream test")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(localIP.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	streamTarget.Listener = listener
	streamTarget.Start()
	defer streamTarget.Close()
	proxy := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", httpPort)}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	var stream *http.Response
	var reader *bufio.Reader
	for _, node := range []string{"one", "two"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		result, err := manager.Switch(ctx, "resproxy-main", node, json.RawMessage(`{"protocol":"freedom","settings":{}}`))
		if err != nil {
			cancel()
			t.Fatalf("live switch %s failed: %v\n%s", node, err, output.String())
		}
		if !result.Changed || process.Process.Pid != pid {
			t.Fatal("live selector did not change within the same Xray process")
		}
		if node == "one" {
			stream, err = client.Get(streamTarget.URL)
			if err != nil {
				t.Fatalf("open stream before switch: %v", err)
			}
			defer stream.Body.Close()
			reader = bufio.NewReader(stream.Body)
			if line, err := reader.ReadString('\n'); err != nil || line != "before\n" {
				t.Fatalf("first streamed response: %q %v", line, err)
			}
		}
		if node == "two" {
			replacedTag = result.PreviousTag
			close(streamGate)
			if line, err := reader.ReadString('\n'); err != nil || line != "after\n" {
				t.Fatalf("existing stream did not survive switch: %q %v", line, err)
			}
			if err := manager.Retire(ctx, "resproxy-main", result.SelectedTag); err == nil {
				t.Fatal("selected outbound was retired")
			}
			if err := manager.Retire(ctx, "resproxy-main", replacedTag); err != nil {
				t.Fatalf("old outbound retirement failed: %v", err)
			}
			if err := manager.Retire(ctx, "resproxy-main", replacedTag); err != nil {
				t.Fatalf("repeated old outbound retirement failed: %v", err)
			}
		}
		cancel()
	}
	if replacedTag == "" || process.Process.Pid != pid {
		t.Fatal("old handler was not replaced in the same Xray process")
	}
}

func TestRealXrayIsolatedRefreshProbe(t *testing.T) {
	binary := testBinary(t)
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	base := testBase(t)
	var config struct {
		Inbounds []struct {
			Tag  string `json:"tag"`
			Port int    `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(base, &config); err != nil {
		t.Fatal(err)
	}
	ports := make(map[string]int)
	for _, inbound := range config.Inbounds {
		ports[inbound.Tag] = inbound.Port
	}
	path := filepath.Join(t.TempDir(), "base.json")
	if err := os.WriteFile(path, base, 0o600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(binary, "run", "-c", path)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Process.Kill(); _ = process.Wait() }()
	manager := NewManager(binary, fmt.Sprintf("127.0.0.1:%d", ports["resproxy-api"]), t.TempDir())
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := manager.Current(ctx, RefreshProbeTag)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe API unavailable: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.Restore(ctx, RefreshProbeTag, "block"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Switch(ctx, RefreshProbeTag, "working", json.RawMessage(`{"protocol":"freedom","settings":{}}`)); err != nil {
		t.Fatal(err)
	}
	proxy := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", ports[RefreshProbeTag])}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("probe traffic did not pass through selected outbound: %d", response.StatusCode)
	}
}

// Exercise the large-table RPC against the bundled core on the ARM lab.
func TestRealXrayLargeRoutingReloadPreservesSelectors(t *testing.T) {
	binary := testBinary(t)
	base := DefaultBaseConfig()
	base.APIPort, base.HTTPPort, base.TransparentPort = freePort(t), freePort(t), freePort(t)
	base.ProbeBasePort = 25000
	body, err := RenderBase(base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "base.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(binary, "run", "-c", path)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Process.Kill(); _ = process.Wait() }()
	manager := NewManager(binary, fmt.Sprintf("127.0.0.1:%d", base.APIPort), t.TempDir())
	deadline := time.Now().Add(20 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := manager.Current(ctx, "resproxy-main")
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("API unavailable: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 330*time.Second)
	defer cancel()
	result, err := manager.Switch(ctx, "resproxy-main", "live", json.RawMessage(`{"protocol":"freedom","settings":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	next := base
	next.Policies = append([]BasePolicy(nil), base.Policies...)
	cidrs := make([]string, 20000)
	for i := range cidrs {
		cidrs[i] = fmt.Sprintf("198.18.%d.%d/32", i/256, i%256)
	}
	next.Policies[0].Rules = []RouteRule{{ID: "large", Action: "direct", Country: "ru", UseGeoIP: true, CountryCIDRs: cidrs}}
	started := time.Now()
	if err := manager.PublishRouting(ctx, base, next); err != nil {
		t.Fatal(err)
	}
	for _, tag := range balancerTags(next) {
		selected, err := manager.Current(ctx, tag)
		expected := "block"
		if tag == "resproxy-main" {
			expected = result.SelectedTag
		}
		if err != nil || selected != expected {
			t.Fatalf("%s selected %q, expected %q: %v", tag, selected, expected, err)
		}
	}
	if err := manager.PublishRouting(ctx, next, base); err != nil {
		t.Fatal(err)
	}
	selected, err := manager.Current(ctx, "resproxy-main")
	if err != nil || selected != result.SelectedTag {
		t.Fatalf("restore selected %q: %v", selected, err)
	}
	t.Logf("20000 CIDRs published and restored in %s; Xray PID %d stayed active", time.Since(started), process.Process.Pid)
}
