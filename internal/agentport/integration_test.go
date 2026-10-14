package agentport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

func testPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestRealARMHealthSelectsFirstWorkingNode(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("health readiness validation requires Linux /proc")
	}
	binary := os.Getenv("RESPROXY_TEST_XRAY")
	if binary == "" {
		t.Skip("set RESPROXY_TEST_XRAY for real Xray integration test")
	}
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	previousTargets := healthTargets
	healthTargets = []struct {
		label  string
		url    string
		status int
	}{{"local-204", target.URL, http.StatusNoContent}}
	defer func() { healthTargets = previousTargets }()

	config := xray.DefaultBaseConfig()
	config.APIPort = testPort(t)
	config.TransparentPort = testPort(t)
	config.HTTPPort = testPort(t)
	config.SocksPort = testPort(t)
	config.DNSPolicyBasePort = 23600
	config.ProbeBasePort = 23500
	body, err := xray.RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	basePath := filepath.Join(root, "base.json")
	if err := os.WriteFile(basePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(binary, "run", "-c", basePath)
	var childLog bytes.Buffer
	child.Stdout, child.Stderr = &childLog, &childLog
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	manager := xray.NewManager(binary, fmt.Sprintf("127.0.0.1:%d", config.APIPort), root)
	deadline := time.Now().Add(15 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := manager.Current(ctx, "resproxy-main")
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = child.Process.Kill()
			_ = child.Wait()
			t.Fatalf("Xray API did not start: %v; %s", err, childLog.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	readyPath := filepath.Join(root, "xray-ready")
	if err := os.WriteFile(readyPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	poolPath := filepath.Join(root, "pool.json")
	pool := healthPool{
		Version: 4, Policies: map[string][]string{"resproxy-main": {"good"}},
		HealthPolicies: map[string]healthPolicyContract{"resproxy-main": {
			Policy: healthPolicy{ActiveLivenessSeconds: 3,
				ProbeBatchSize: 3},
			Mode: "best", Candidates: []string{"good"},
			Groups: []healthGroup{{Selector: "country:CA", Members: []string{"good"}}},
			Nodes:  map[string]healthNode{"good": {Label: "test direct", Country: "CA"}},
		}},
		PolicyPrefixes:   map[string]string{"resproxy-main": "resproxy-main-"},
		BaseOutboundTags: []string{"block", "direct-wan"},
		Outbounds:        map[string]json.RawMessage{"good": json.RawMessage(`{"protocol":"freedom","settings":{}}`)},
	}
	if err := writeJSONAtomic(poolPath, pool); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	healthDone := make(chan struct{})
	defer func() {
		cancel()
		select {
		case <-healthDone:
		case <-time.After(5 * time.Second):
			t.Error("health test workers did not stop")
		}
	}()
	go func() {
		defer close(healthDone)
		_ = RunHealth(ctx, Options{
			StateRoot: filepath.Join(root, "state"), XrayBinary: binary,
			XrayAPIServer: manager.Server, XrayReadyFile: readyPath,
			HealthPoolFile: poolPath, HotRuntimeReadyFile: filepath.Join(root, "hot-ready.json"),
			ProbeURL:      fmt.Sprintf("http://127.0.0.1:%d", config.ProbeBasePort),
			ProbeBasePort: config.ProbeBasePort, HealthInterval: time.Second,
		})
	}()
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var state healthState
		if readJSON(statePath(filepath.Join(root, "state"), "selector-health"), &state) == nil {
			item := state["resproxy-main"]
			if item != nil && item.Selected == "good" && item.RuntimeConfirmed {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("health controller did not confirm the working candidate")
}
