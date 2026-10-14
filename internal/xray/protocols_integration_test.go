package xray

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
	"github.com/Ati054/sb-gateway-openwrt/internal/sip003"
	"github.com/Ati054/sb-gateway-openwrt/internal/subscription"
)

func TestRealXrayAdditionalProtocolsTCPUDPAndLiveSwitch(t *testing.T) {
	binaryPath := testBinary(t)
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
		t.Fatal("private non-loopback address required")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(localIP.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "protocol-traffic-ok") }))
	target.Listener = listener
	target.Start()
	defer target.Close()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: localIP})
	if err != nil {
		t.Fatal(err)
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
	tmp := t.TempDir()
	uuid := "11111111-1111-1111-1111-111111111111"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(tmp, "cert.pem"), filepath.Join(tmp, "key.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600)
	pin := sha256.Sum256(der)
	vmPort, trPort, ssPort, newPort := freePort(t), freePort(t), freePort(t), freePort(t)
	ss2022 := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	inbounds := []any{
		map[string]any{"listen": "127.0.0.1", "port": vmPort, "protocol": "vmess", "settings": map[string]any{"clients": []any{map[string]any{"id": uuid}}}},
		map[string]any{"listen": "127.0.0.1", "port": trPort, "protocol": "trojan", "settings": map[string]any{"clients": []any{map[string]any{"password": "trojan-test"}}}, "streamSettings": map[string]any{"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"certificates": []any{map[string]any{"certificateFile": certPath, "keyFile": keyPath}}}}},
		map[string]any{"listen": "127.0.0.1", "port": ssPort, "protocol": "shadowsocks", "settings": map[string]any{"method": "aes-256-gcm", "password": "ss-test", "network": "tcp,udp"}},
		map[string]any{"listen": "127.0.0.1", "port": newPort, "protocol": "shadowsocks", "settings": map[string]any{"method": "2022-blake3-aes-128-gcm", "password": ss2022, "network": "tcp,udp"}},
	}
	start := func(name string, config []byte) *exec.Cmd {
		t.Helper()
		runTest(t, binaryPath, config)
		path := filepath.Join(tmp, name+".json")
		if err := os.WriteFile(path, config, 0600); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(tmp, name+".log")
		logFile, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binaryPath, "run", "-c", path)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cmd.Process.Kill()
			cmd.Wait()
			logFile.Close()
			if t.Failed() {
				data, _ := os.ReadFile(logPath)
				t.Logf("%s: %s", name, data)
			}
		})
		return cmd
	}
	serverConfig, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "debug"}, "inbounds": inbounds, "outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []string{localIP.String() + "/32"}}}}}}})
	start("protocol-server", serverConfig)
	base := DefaultBaseConfig()
	base.APIPort = freePort(t)
	base.HTTPPort = freePort(t)
	base.SocksPort = freePort(t)
	base.TransparentPort = freePort(t)
	base.ProbeBasePort = 29000
	base.DNSPolicyBasePort = 29100
	body, err := RenderBase(base)
	if err != nil {
		t.Fatal(err)
	}
	var debugConfig map[string]any
	json.Unmarshal(body, &debugConfig)
	debugConfig["log"] = map[string]any{"loglevel": "debug"}
	body, _ = json.Marshal(debugConfig)
	clientProcess := start("protocol-client", body)
	pid := clientProcess.Process.Pid
	os.MkdirAll(filepath.Join(tmp, "manager"), 0700)
	plugins := sip003.NewManager()
	defer plugins.Close()
	manager := NewManager(binaryPath, fmt.Sprintf("127.0.0.1:%d", base.APIPort), filepath.Join(tmp, "manager"))
	manager.PrepareOutbound = plugins.Prepare
	manager.ReleaseOutbound = plugins.Release
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = manager.Current(ctx, "resproxy-main")
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	feed := fmt.Sprintf("vmess://%s@127.0.0.1:%d#DE%%20VMess\ntrojan://trojan-test@127.0.0.1:%d?sni=localhost&pcs=%s#DE%%20Trojan\nss://aes-256-gcm:ss-test@127.0.0.1:%d#DE%%20SS\nss://2022-blake3-aes-128-gcm:%s@127.0.0.1:%d#DE%%20SS2022", uuid, vmPort, trPort, hex.EncodeToString(pin[:]), ssPort, url.QueryEscape(ss2022), newPort)
	if os.Getenv("RESPROXY_TEST_PLUGINS") == "1" {
		pluginPort := freePort(t)
		child := exec.Command("/usr/bin/v2ray-plugin")
		child.Env = append(os.Environ(), "SS_REMOTE_HOST=127.0.0.1", "SS_REMOTE_PORT="+fmt.Sprint(pluginPort), "SS_LOCAL_HOST=127.0.0.1", "SS_LOCAL_PORT="+fmt.Sprint(ssPort), "SS_PLUGIN_OPTIONS=server;mode=websocket;path=/plugin", "GOMEMLIMIT=16MiB")
		child.Stdout, child.Stderr = io.Discard, io.Discard
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { child.Process.Kill(); child.Wait() })
		time.Sleep(200 * time.Millisecond)
		feed += fmt.Sprintf("\nss://aes-256-gcm:ss-test@127.0.0.1:%d/?plugin=%s#DE%%20SSPlugin", pluginPort, url.QueryEscape("v2ray-plugin;mode=websocket;path=/plugin"))
	}
	nodes, err := subscription.Parse("protocol-test", []byte(feed), 10)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _, err = catalog.Reconcile("protocol-test", nil, nodes)
	if err != nil {
		t.Fatal(err)
	}
	store := generation.Store{Root: filepath.Join(tmp, "store")}
	id, err := store.Stage(nodes)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err = store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", base.HTTPPort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	for _, node := range nodes {
		t.Run(node.Label, func(t *testing.T) {
			outbound, err := RenderOutbound(node)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := manager.Switch(ctx, "resproxy-main", node.ID, outbound); err != nil {
				t.Fatal(err)
			}
			transport.CloseIdleConnections()
			response, err := client.Get(target.URL)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || string(got) != "protocol-traffic-ok" {
				t.Fatal("TCP proxy response mismatch", err)
			}
			if node.Settings["plugin"] == nil {
				testProtocolUDP(t, base.SocksPort, udp.LocalAddr().(*net.UDPAddr))
			}
			if clientProcess.Process.Pid != pid {
				t.Fatal("switch restarted Xray")
			}
		})
	}
}

func testProtocolUDP(t *testing.T, port int, target *net.UDPAddr) {
	t.Helper()
	control, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	control.SetDeadline(time.Now().Add(4 * time.Second))
	control.Write([]byte{5, 1, 0})
	var auth [2]byte
	if _, err := io.ReadFull(control, auth[:]); err != nil || auth != [2]byte{5, 0} {
		t.Fatal("SOCKS authentication failed", err)
	}
	control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	var reply [10]byte
	if _, err := io.ReadFull(control, reply[:]); err != nil || reply[1] != 0 || reply[3] != 1 {
		t.Fatal("SOCKS UDP associate failed", err)
	}
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(binary.BigEndian.Uint16(reply[8:]))})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(4 * time.Second))
	message := []byte("udp-protocol-ok")
	frame := []byte{0, 0, 0, 1}
	frame = append(frame, target.IP.To4()...)
	frame = binary.BigEndian.AppendUint16(frame, uint16(target.Port))
	frame = append(frame, message...)
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 2048)
	n, err := conn.Read(b)
	if err != nil || n < 10 || string(b[10:n]) != string(message) {
		t.Fatal("UDP proxy response mismatch", err)
	}
}
