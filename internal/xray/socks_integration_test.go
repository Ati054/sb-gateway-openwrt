package xray

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestRealXraySOCKSTrafficConditionsAndInternalIsolation(t *testing.T) {
	binary := testBinary(t)
	var localIP net.IP
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addresses {
		if p, ok := a.(*net.IPNet); ok && p.IP.To4() != nil && p.IP.IsPrivate() && !p.IP.IsLoopback() {
			localIP = p.IP
			break
		}
	}
	if localIP == nil {
		t.Skip("private non-loopback address required")
	}
	target := func() *httptest.Server {
		listener, e := net.Listen("tcp", net.JoinHostPort(localIP.String(), "0"))
		if e != nil {
			t.Fatal(e)
		}
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "conditional-direct-ok") }))
		server.Listener = listener
		server.Start()
		t.Cleanup(server.Close)
		return server
	}
	allowed, denied := target(), target()
	dest, _ := url.Parse(allowed.URL)
	cfg := DefaultBaseConfig()
	cfg.APIPort = freePort(t)
	cfg.HTTPPort = freePort(t)
	cfg.SocksPort = freePort(t)
	cfg.TransparentPort = freePort(t)
	cfg.ProbeBasePort = freePortRange(t, cfg.ProbeLaneCount()+1)
	cfg.DNSPolicyBasePort = freePortRange(t, len(cfg.Policies))
	cfg.Policies[0].Rules = []RouteRule{{ID: "conditional", Name: "Тест портов", Action: "direct", Ports: dest.Port(), Network: "tcp", Protocols: []string{"http"}, Inbounds: []string{"socks"}}}
	body, err := RenderBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runTest(t, binary, body)
	path := filepath.Join(t.TempDir(), "socks.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(binary, "run", "-c", path)
	coreLog, err := os.Create(filepath.Join(filepath.Dir(path), "core.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer coreLog.Close()
	process.Stdout, process.Stderr = coreLog, coreLog
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = process.Wait(); close(exited) }()
	defer func() { _ = process.Process.Kill(); <-exited }()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.SocksPort))
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-exited:
			body, _ := os.ReadFile(coreLog.Name())
			t.Fatalf("SOCKS core exited: %s\n%s", process.ProcessState, body)
		default:
		}
		c, e := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if e == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			body, _ := os.ReadFile(coreLog.Name())
			t.Fatalf("SOCKS not listening: %v\n%s", e, body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	proxy, _ := url.Parse("socks5://" + addr)
	transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", allowed.URL, nil)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("matching SOCKS traffic did not pass direct rule", err)
	}
	got, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(got) != "conditional-direct-ok" {
		t.Fatal(string(got))
	}
	if response, e := client.Get(denied.URL); e == nil {
		response.Body.Close()
		t.Fatal("unmatched port bypassed blocked default")
	}
	internal, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", cfg.HTTPPort))
	internalTransport := &http.Transport{Proxy: http.ProxyURL(internal)}
	defer internalTransport.CloseIdleConnections()
	if response, e := (&http.Client{Transport: internalTransport, Timeout: 3 * time.Second}).Get(allowed.URL); e == nil && response.StatusCode == 200 {
		response.Body.Close()
		t.Fatal("internal HTTP fetch matched user SOCKS rule")
	} else if response != nil {
		response.Body.Close()
	}
}
