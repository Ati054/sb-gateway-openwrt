package xray

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Use ordinary dokodemo-door sockets with the production transparent tags.
// This exercises real Xray source/rule routing without host TPROXY setup.
func TestRealXraySelectedDevicesOverrideAllDeviceList(t *testing.T) {
	binary := testBinary(t)
	marker := func(label string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, label) }))
		t.Cleanup(s.Close)
		return s
	}
	commonTarget, deviceTarget := marker("all-devices"), marker("selected-device")
	for _, commonFirst := range []bool{true, false} {
		t.Run(strconv.FormatBool(commonFirst), func(t *testing.T) {
			cfg := DefaultBaseConfig()
			cfg.APIPort, cfg.HTTPPort, cfg.SocksPort, cfg.TransparentPort = freePort(t), freePort(t), freePort(t), freePort(t)
			cfg.ProbeBasePort, cfg.DNSPolicyBasePort = 33000, 34000
			common := BasePolicy{ID: "main", Rules: []RouteRule{{ID: "common", Action: "direct", Ports: "23456"}}}
			device := BasePolicy{ID: "office", SourceCIDRs: []string{"127.0.0.2/32", "::1/128"}, Rules: []RouteRule{{ID: "device", Action: "proxy", Ports: "23456"}}}
			cfg.Policies = []BasePolicy{common, device}
			if !commonFirst {
				cfg.Policies = []BasePolicy{device, common}
			}
			cfg.DefaultPolicy = "main"
			body, err := RenderBase(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err = json.Unmarshal(body, &config); err != nil {
				t.Fatal(err)
			}
			for _, raw := range config["inbounds"].([]any) {
				in := raw.(map[string]any)
				if in["tag"] == "resproxy-transparent" || in["tag"] == "resproxy-transparent6" {
					in["listen"] = "127.0.0.1"
					if in["tag"] == "resproxy-transparent6" {
						in["listen"] = "::1"
					}
					in["settings"] = map[string]any{"network": "tcp", "address": "192.0.2.1", "port": 23456}
					delete(in, "streamSettings")
				}
			}
			config["outbounds"] = append(config["outbounds"].([]any), map[string]any{
				"tag": "test-device", "protocol": "freedom", "settings": map[string]any{"redirect": strings.TrimPrefix(deviceTarget.URL, "http://")},
			}, map[string]any{
				"tag": "test-main", "protocol": "freedom", "settings": map[string]any{"redirect": strings.TrimPrefix(commonTarget.URL, "http://")},
			})
			for _, raw := range config["outbounds"].([]any) {
				out := raw.(map[string]any)
				if out["tag"] == "direct-wan" {
					out["settings"] = map[string]any{"redirect": strings.TrimPrefix(commonTarget.URL, "http://")}
				}
			}
			for _, raw := range config["routing"].(map[string]any)["balancers"].([]any) {
				b := raw.(map[string]any)
				if b["tag"] == "resproxy-main" {
					b["selector"] = []string{"test-main"}
				}
				if b["tag"] == "resproxy-office" {
					b["selector"] = []string{"test-device"}
				}
			}
			body, err = json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			runTest(t, binary, body)
			path := filepath.Join(t.TempDir(), "devices.json")
			if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			process := exec.Command(binary, "run", "-c", path)
			if err = process.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { process.Process.Kill(); process.Wait() }()
			address := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.TransparentPort))
			deadline := time.Now().Add(10 * time.Second)
			for {
				c, e := net.DialTimeout("tcp", address, 100*time.Millisecond)
				if e == nil {
					c.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("transparent test socket unavailable", e)
				}
				time.Sleep(50 * time.Millisecond)
			}
			for _, check := range []struct{ source, want string }{
				{"127.0.0.2", "selected-device"}, {"127.0.0.3", "all-devices"}, {"::1", "selected-device"},
			} {
				t.Run(check.source, func(t *testing.T) {
					host := "127.0.0.1"
					if strings.Contains(check.source, ":") {
						host = "::1"
					}
					dialer := net.Dialer{Timeout: 3 * time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(check.source)}}
					transport := &http.Transport{DialContext: dialer.DialContext}
					defer transport.CloseIdleConnections()
					client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
					response, e := client.Get("http://" + net.JoinHostPort(host, strconv.Itoa(cfg.TransparentPort)) + "/")
					if e != nil {
						t.Fatal(e)
					}
					got, e := io.ReadAll(response.Body)
					response.Body.Close()
					if e != nil || string(got) != check.want {
						t.Fatalf("source %s got %q want %s: %v", check.source, got, check.want, e)
					}
				})
			}
		})
	}
}
