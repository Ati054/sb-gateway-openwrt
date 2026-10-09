package xray

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"example.org/resilient-proxy/internal/subscription"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRealXrayHysteriaAgainstOfficialServer(t *testing.T) {
	reference := os.Getenv("SB_TEST_HYSTERIA_REFERENCE")
	if reference == "" {
		t.Skip("official server opt-in")
	}
	binary := testBinary(t)
	dir := t.TempDir()
	port := freePort(t)
	serverLog, e := os.Create(filepath.Join(dir, "synthetic-server.log"))
	if e != nil {
		t.Fatal(e)
	}
	defer serverLog.Close()
	server := exec.Command(reference, "--local-server", dir, fmt.Sprint(port))
	server.Stdout = serverLog
	server.Stderr = serverLog
	if e = server.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { server.Process.Kill(); server.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, e = os.Stat(filepath.Join(dir, "ready")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("official server startup timeout")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cp, e := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if e != nil {
		t.Fatal(e)
	}
	block, _ := pem.Decode(cp)
	if block == nil {
		t.Fatal("certificate invalid")
	}
	pin := sha256.Sum256(block.Bytes)
	feed := fmt.Sprintf("hysteria2://local-control-only@127.0.0.1:%d?sni=localhost&pcs=%s#DE%%20local%%20control", port, hex.EncodeToString(pin[:]))
	nodes, e := subscription.Parse("synthetic-hysteria", []byte(feed), 10)
	if e != nil || len(nodes) != 1 {
		t.Fatal("import failed", e)
	}
	out, e := RenderOutbound(nodes[0])
	if e != nil {
		t.Fatal(e)
	}
	var outbound map[string]any
	if json.Unmarshal(out, &outbound) != nil {
		t.Fatal("render invalid")
	}
	var ip net.IP
	addresses, _ := net.InterfaceAddrs()
	for _, a := range addresses {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() && !n.IP.IsLoopback() {
			ip = n.IP
			break
		}
	}
	if ip == nil {
		t.Fatal("private address missing")
	}
	listener, e := net.Listen("tcp4", net.JoinHostPort(ip.String(), "0"))
	if e != nil {
		t.Fatal(e)
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hysteria-control-ok") }))
	target.Listener = listener
	target.Start()
	defer target.Close()
	udp, e := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
	if e != nil {
		t.Fatal(e)
	}
	defer udp.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			n, a, e := udp.ReadFromUDP(b)
			if e != nil {
				return
			}
			udp.WriteToUDP(b[:n], a)
		}
	}()
	httpPort, socksPort := freePort(t), freePort(t)
	config, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "debug"}, "inbounds": []any{
		map[string]any{"listen": "127.0.0.1", "port": httpPort, "protocol": "http"}, map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"udp": true}}}, "outbounds": []any{outbound}})
	runTest(t, binary, config)
	path := filepath.Join(dir, "client.json")
	os.WriteFile(path, config, 0600)
	log, e := os.Create(filepath.Join(dir, "synthetic-client.log"))
	if e != nil {
		t.Fatal(e)
	}
	defer log.Close()
	child := exec.Command(binary, "run", "-c", path)
	child.Stdout = log
	child.Stderr = log
	if e = child.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		child.Process.Kill()
		child.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(dir, "synthetic-client.log"))
			t.Log(string(b))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", httpPort), time.Second)
		if e == nil {
			c.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("client startup timeout")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", httpPort))
	tr := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, Timeout: 15 * time.Second}
	resp, e := client.Get(target.URL)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || string(b) != "hysteria-control-ok" {
		t.Fatal("TCP control failed", e)
	}
	t.Log("official-server TCP proxy passed via imported and rendered Hysteria2 node")
	testProtocolUDP(t, socksPort, udp.LocalAddr().(*net.UDPAddr))
	t.Log("official-server UDP proxy passed")
}
