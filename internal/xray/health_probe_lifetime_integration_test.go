package xray

import (
	"bufio"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A TCP port can be open while REALITY never sends a ServerHello. Closing the
// HTTP probe client must not leave its core dial and sockets alive indefinitely.
func TestRealHealthProbeReleasesStalledREALITYHandshake(t *testing.T) {
	binary := testBinary(t)
	server, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var active, accepted atomic.Int32
	go func() {
		for {
			c, e := server.Accept()
			if e != nil {
				return
			}
			active.Add(1)
			accepted.Add(1)
			go func() {
				defer active.Add(-1)
				defer c.Close()
				c.SetReadDeadline(time.Now().Add(20 * time.Second))
				io.Copy(io.Discard, c)
			}()
		}
	}()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	config := map[string]any{
		"log":       map[string]any{"loglevel": "error"},
		"inbounds":  []any{map[string]any{"tag": "outbound-health-background", "listen": "127.0.0.1", "port": port, "protocol": "http", "settings": map[string]any{}}},
		"outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": server.Addr().(*net.TCPAddr).Port, "users": []any{map[string]any{"id": "11111111-1111-1111-1111-111111111111", "encryption": "none", "flow": "xtls-rprx-vision"}}}}}, "streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"serverName": "example.org", "fingerprint": "chrome", "publicKey": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "shortId": ""}}}},
	}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	runTest(t, binary, body)
	dir := t.TempDir()
	path := filepath.Join(dir, "core.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	logfile, err := os.Create(filepath.Join(dir, "core.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logfile.Close()
	child := exec.Command(binary, "run", "-c", path)
	child.Stdout = logfile
	child.Stderr = logfile
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, e := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if e == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("core startup timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		c, e := net.DialTimeout("tcp", addr, time.Second)
		if e != nil {
			t.Fatal(e)
		}
		c.SetDeadline(time.Now().Add(time.Second))
		fmt.Fprint(c, "CONNECT example.org:443 HTTP/1.1\r\nHost: example.org:443\r\n\r\n")
		if line, e := bufio.NewReader(c).ReadString('\n'); e != nil || line != "HTTP/1.1 200 Connection established\r\n" {
			c.Close()
			t.Fatalf("CONNECT: %q %v", line, e)
		}
		// Let the upstream handshake start, then emulate the controller's timeout.
		time.Sleep(100 * time.Millisecond)
		c.Close()
	}
	if accepted.Load() < 1 {
		t.Fatal("no real upstream handshake exercised")
	}
	deadline = time.Now().Add(10 * time.Second)
	for active.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if active.Load() != 0 {
		t.Fatalf("%d stalled upstream sockets survived probe cancellation", active.Load())
	}
	t.Logf("%d stalled handshake sockets released by the core deadline", accepted.Load())
}
