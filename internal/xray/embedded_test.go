package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	router "github.com/xtls/xray-core/app/router/command"
)

func TestEmbeddedCoreLiveTransactionsAndLifecycle(t *testing.T) {
	// Real core, handlers, API and HTTP traffic: a missing executable is deliberate.
	config := DefaultBaseConfig()
	config.ProbeBudget = 1
	ports := []*int{&config.APIPort, &config.TransparentPort, &config.HTTPPort, &config.SocksPort, &config.ProbeBasePort, &config.DNSPolicyBasePort}
	for i, p := range ports {
		*p = 30000 + i*100
	}
	body, err := RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	server := fmt.Sprintf("127.0.0.1:%d", config.APIPort)
	engine, err := StartEmbedded(body, server)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			engine.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	manager := NewManager("/no/such/xray", server, t.TempDir())
	manager.Run = engine.Command
	if err := manager.Restore(ctx, "resproxy-main", "block"); err != nil {
		t.Fatal(err)
	}
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", config.ProbeBasePort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "embedded-core-traffic") }))
	defer origin.Close()
	if resp, err := client.Get(origin.URL); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("initial fail-closed selector passed traffic")
		}
	}
	result, err := manager.Switch(ctx, "resproxy-main", "node-a", json.RawMessage(`{"protocol":"freedom","settings":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Restore(ctx, "outbound-health-probe", result.SelectedTag); err != nil {
		t.Fatal(err)
	}
	verify := func() {
		t.Helper()
		resp, err := client.Get(origin.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(body) != "embedded-core-traffic" {
			t.Fatalf("traffic %q %v", body, err)
		}
	}
	verify()
	next := config
	next.Policies = append([]BasePolicy(nil), config.Policies...)
	next.Policies[0].Rules = []RouteRule{{ID: "local", Action: "direct", Domains: []string{"example.org"}}}
	if err := manager.PublishRouting(ctx, config, next); err != nil {
		t.Fatal(err)
	}
	actual, err := manager.current(ctx, "resproxy-main")
	if err != nil || actual != result.SelectedTag {
		t.Fatalf("override lost: %q %v", actual, err)
	}
	verify()
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := engine.Command(cancelled, "", "api", "bo", "--server="+server, "-b", "resproxy-main", "block"); err == nil {
		t.Fatal("cancelled override accepted")
	}
	actual, _ = manager.current(ctx, "resproxy-main")
	if actual != result.SelectedTag {
		t.Fatal("cancelled operation changed selection")
	}
	inventory, err := engine.Command(ctx, "", "api", "lso", "--server="+server)
	if err != nil || !strings.Contains(string(inventory), result.SelectedTag) || strings.Contains(string(inventory), "proxySettings") {
		t.Fatalf("inventory projection %s %v", inventory, err)
	}
	second, err := manager.Switch(ctx, "resproxy-main", "node-b", json.RawMessage(`{"protocol":"freedom","settings":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Restore(ctx, "outbound-health-probe", second.SelectedTag); err != nil {
		t.Fatal(err)
	}
	if err := manager.Retire(ctx, "resproxy-main", second.PreviousTag); err != nil {
		t.Fatal(err)
	}
	verify()
	path := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(path, []byte(`{"routing":{"rules":[{"type":"field","outboundTag":"direct"}]}}`), 0600)
	before, _ := router.NewRoutingServiceClient(engine.conn).ListRule(ctx, &router.ListRuleRequest{})
	if _, err := engine.Command(ctx, "", "api", "adrules", "--server="+server, path); err == nil {
		t.Fatal("invalid routing accepted")
	}
	after, _ := router.NewRoutingServiceClient(engine.conn).ListRule(ctx, &router.ListRuleRequest{})
	if len(before.GetRules()) != len(after.GetRules()) {
		t.Fatal("rejected routing changed rules")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	for _, port := range []int{config.APIPort, config.HTTPPort, config.SocksPort, config.ProbeBasePort} {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("core leaked port %d: %v", port, err)
		}
		l.Close()
	}
}
