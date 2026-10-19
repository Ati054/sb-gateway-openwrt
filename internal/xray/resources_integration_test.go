package xray

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the real worker lifetimes, not an outbound completion callback:
// API and health probes must not inflate the number of client sessions.
func TestRealXrayClientSessionGauges(t *testing.T) {
	binary := testBinary(t)
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	udpTarget, err := net.ListenPacket("udp4", target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer udpTarget.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			n, a, err := udpTarget.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = udpTarget.WriteTo(b[:n], a)
		}
	}()
	base := testBase(t)
	var config map[string]any
	if err := json.Unmarshal(base, &config); err != nil {
		t.Fatal(err)
	}
	ports := map[string]int{}
	for _, raw := range config["inbounds"].([]any) {
		inbound := raw.(map[string]any)
		tag := inbound["tag"].(string)
		ports[tag] = int(inbound["port"].(float64))
		if tag == "resproxy-transparent" {
			// Only the socket setup differs: an ordinary UDP fixture needs no root
			// TProxy capability; the production inbound tag and workers are retained.
			inbound["settings"] = map[string]any{"address": "192.0.2.10", "port": target.Addr().(*net.TCPAddr).Port, "network": "tcp,udp"}
			delete(inbound, "streamSettings")
			delete(inbound, "sniffing")
		}
	}
	config["outbounds"] = append(config["outbounds"].([]any), map[string]any{
		"tag": "session-fixture", "protocol": "freedom", "settings": map[string]any{"redirect": target.Addr().String()},
	})
	routing := config["routing"].(map[string]any)
	rules := routing["rules"].([]any)
	clientTags := append([]string{"resproxy-transparent", "resproxy-http"}, HealthProbeTags()...)
	routing["rules"] = append([]any{map[string]any{"type": "field", "inboundTag": clientTags, "outboundTag": "session-fixture"}}, rules...)
	// Expiry must be observed during this integration test. The production
	// timeout is independently asserted in the base configuration tests.
	config["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{"connIdle": 2, "uplinkOnly": 1, "downlinkOnly": 1}}}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "base.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(filepath.Join(dir, "core.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	process := exec.Command(binary, "run", "-c", path)
	process.Stdout, process.Stderr = output, output
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Process.Kill(); _ = process.Wait() }()
	api, err := NewAPIClient(testAPIAddress(t, base))
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	await := func(tcp, udp int64) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		var values map[string]int64
		var last error
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			data, err := api.Command(ctx, "", "api", "statsquery")
			cancel()
			last = err
			var counters []struct {
				Name  string `json:"name"`
				Value int64  `json:"value"`
			}
			if err == nil {
				last = json.Unmarshal(data, &counters)
			}
			values = map[string]int64{}
			for _, c := range counters {
				values[c.Name] = c.Value
			}
			_, hasTCP := values["sb-gateway>>>active>>>tcp"]
			_, hasUDP := values["sb-gateway>>>active>>>udp"]
			if last == nil && hasTCP && hasUDP && values["sb-gateway>>>active>>>tcp"] == tcp && values["sb-gateway>>>active>>>udp"] == udp {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		log, _ := os.ReadFile(filepath.Join(dir, "core.log"))
		t.Fatalf("sessions did not reach TCP=%d UDP=%d: %v, %v\n%s", tcp, udp, values, last, log)
	}
	await(0, 0)
	exchange := func(c net.Conn) {
		t.Helper()
		_ = c.SetDeadline(time.Now().Add(time.Second))
		if _, err := c.Write([]byte("alive")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 5)
		if _, err := io.ReadFull(c, got); err != nil || string(got) != "alive" {
			t.Fatalf("echo %q: %v", got, err)
		}
	}
	for cycle := 0; cycle < 3; cycle++ {
		var clients []net.Conn
		defer func() {
			for _, c := range clients {
				c.Close()
			}
		}()
		for i := 0; i < 8; i++ {
			c, err := net.Dial("tcp4", fmt.Sprintf("127.0.0.1:%d", ports["resproxy-transparent"]))
			if err != nil {
				t.Fatal(err)
			}
			clients = append(clients, c)
			exchange(c)
		}
		for i := 0; i < 5; i++ {
			c, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", ports["resproxy-transparent"]))
			if err != nil {
				t.Fatal(err)
			}
			clients = append(clients, c)
			exchange(c)
		}
		await(8, 5)
		// A health probe uses a real HTTP CONNECT stream but is not a
		// client session and must leave both gauges unchanged.
		probe := HealthProbeTags()[0]
		if ports[probe] == 0 {
			t.Fatal("health probe inbound missing")
		}
		c, err := net.Dial("tcp4", fmt.Sprintf("127.0.0.1:%d", ports[probe]))
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
		_ = c.SetDeadline(time.Now().Add(time.Second))
		if _, err := io.WriteString(c, "CONNECT 192.0.2.10:443 HTTP/1.1\r\nHost: 192.0.2.10:443\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(c)
		status, err := reader.ReadString('\n')
		if err != nil || !strings.Contains(status, " 200 ") {
			t.Fatalf("probe CONNECT %q: %v", status, err)
		}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if line == "\r\n" {
				break
			}
		}
		exchange(c)
		// Several persistent gRPC queries must not count as client TCP sessions.
		await(8, 5)
		for _, c := range clients {
			c.Close()
		}
		await(0, 0)
	}
}
