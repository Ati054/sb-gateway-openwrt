package agentport

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Run in a fresh process so the generated trust anchor cannot change the root
// pool used by unrelated tests. Every socket and core belongs to this fixture.
func TestRealXrayWarmQualityUsesSameTLSConnection(t *testing.T) {
	binary := os.Getenv("RESPROXY_TEST_XRAY")
	if binary == "" {
		t.Skip("set RESPROXY_TEST_XRAY to test the packaged core")
	}
	if os.Getenv("RESPROXY_WARM_TEST_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRealXrayWarmQualityUsesSameTLSConnection$", "-test.v", "-test.timeout=40s")
		cmd.Env = append(os.Environ(), "RESPROXY_WARM_TEST_CHILD=1")
		if body, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated warm integration failed: %v\n%s", err, body)
		}
		return
	}
	root := t.TempDir()
	var calls, connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodHead {
			t.Error("unexpected quality method")
		}
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()
	certificate := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", certificate)
	t.Setenv("SSL_CERT_DIR", root)
	previousTargets := healthTargets
	healthTargets = append(healthTargets[:0:0], healthTargets[0])
	healthTargets[0].url = server.URL
	defer func() { healthTargets = previousTargets }()
	apiPort, p1, p2 := testPort(t), testPort(t), testPort(t)
	api := fmt.Sprintf("127.0.0.1:%d", apiPort)
	inbounds := []any{map[string]any{"tag": "api-in", "listen": "127.0.0.1", "port": apiPort, "protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"}}}
	rules := []any{map[string]any{"type": "field", "inboundTag": []string{"api-in"}, "outboundTag": "api"}}
	balancers := []any{}
	pool := healthPool{Version: 4, ProbeBudget: 2, ProbeLanes: 2, Policies: map[string][]string{}, BaseOutboundTags: []string{"a", "b"}, Outbounds: map[string]json.RawMessage{}}
	for index, port := range []int{p1, p2} {
		tag := fmt.Sprintf("outbound-health-background-%d", index+1)
		inbounds = append(inbounds, map[string]any{"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": "http", "settings": map[string]any{}})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{tag}, "balancerTag": tag})
		balancers = append(balancers, map[string]any{"tag": tag, "selector": []string{"a", "b"}, "strategy": map[string]any{"type": "random"}})
		pool.Policies[tag] = []string{"a", "b"}
	}
	write := func(name string, value any) string {
		t.Helper()
		p := filepath.Join(root, name)
		if err := writeJSONAtomic(p, value); err != nil {
			t.Fatal(err)
		}
		return p
	}
	config := write("core.json", map[string]any{"log": map[string]any{"loglevel": "none"}, "api": map[string]any{"tag": "api", "services": []string{"RoutingService", "HandlerService"}}, "inbounds": inbounds, "outbounds": []any{map[string]any{"tag": "a", "protocol": "freedom"}, map[string]any{"tag": "b", "protocol": "freedom"}}, "routing": map[string]any{"rules": rules, "balancers": balancers}})
	poolPath := write("pool.json", pool)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	core := exec.CommandContext(ctx, binary, "run", "-c", config)
	if err := core.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = core.Process.Kill(); _ = core.Wait() }()
	control, err := newXrayControlClient(api)
	if err != nil {
		t.Fatal(err)
	}
	defer control.close()
	for deadline := time.Now().Add(15 * time.Second); ; {
		if _, err := control.outbounds(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test core did not become ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	lanes := []*xraySelectorRuntime{}
	for index, port := range []int{p1, p2} {
		lane := newXraySelectorRuntime(Options{XrayBinary: binary, XrayAPIServer: api, HealthPoolFile: poolPath, ProbeURL: fmt.Sprintf("http://127.0.0.1:%d", port)})
		lane.control = control
		lane.probeSelector = fmt.Sprintf("outbound-health-background-%d", index+1)
		lanes = append(lanes, lane)
	}
	r := &responsiveSelectorRuntime{selectorRuntime: lanes[0], background: lanes[0], backgrounds: lanes, ctx: ctx, enabled: true, parallelEnabled: true, generationPaths: []string{poolPath}}
	r.generation = generationStamp(r.generationPaths)
	measured := r.ProbeQualityParallelWithMeasurement([]string{"a", "b"}, qualityMeasurementWarm)
	for _, node := range []string{"a", "b"} {
		e := measured[node]
		if !e.OK || e.DelayMS == nil || !validWarmQuality(e.Warm) {
			t.Fatalf("invalid packaged-core warm result %s: %+v warm=%+v", node, e, e.Warm)
		}
	}
	if calls.Load() != 4 || connections.Load() != 2 {
		t.Fatalf("two endpoint sessions: requests=%d TLS connections=%d", calls.Load(), connections.Load())
	}
	if measured["a"].Warm.Wave == 0 || measured["a"].Warm.Wave != measured["b"].Warm.Wave {
		t.Fatal("paired warm wave missing")
	}
}
