package xray

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
)

// Exercise actual inbound sniffing, not a TestRoute request with a supplied
// domain. TCP may be established before a busy client creates its ClientHello.
func TestRuleWANFallbackHandlesDelayedClientHelloAndKeepsStrictDomainsBlocked(t *testing.T) {
	var requests atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, "rule-wan-verified")
	}))
	defer origin.Close()
	targetHost, targetPort, err := net.SplitHostPort(strings.TrimPrefix(origin.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(targetPort)
	cfg := DefaultBaseConfig()
	cfg.APIPort, cfg.TransparentPort, cfg.HTTPPort, cfg.SocksPort = freePort(t), freePort(t), freePort(t), freePort(t)
	cfg.ProbeBudget, cfg.ProbeBasePort, cfg.DNSPolicyBasePort = 1, freePort(t), freePort(t)
	cfg.Policies = []BasePolicy{{ID: "main", Rules: []RouteRule{
		{ID: "strict", Action: "proxy", Domains: []string{"full:blocked.example.com"}},
		{ID: "fallback", Action: "proxy", OutageAction: "wan", Domains: []string{"example.com"}},
	}}}
	body, err := RenderBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	inbounds := []any{}
	for _, raw := range config["inbounds"].([]any) {
		in := raw.(map[string]any)
		if in["tag"] == "resproxy-transparent6" {
			continue
		}
		if in["tag"] == "resproxy-transparent" {
			in["listen"] = "127.0.0.1"
			in["settings"] = map[string]any{"network": "tcp", "address": targetHost, "port": port}
			delete(in, "streamSettings")
		}
		inbounds = append(inbounds, in)
	}
	config["inbounds"] = inbounds
	for _, raw := range config["outbounds"].([]any) {
		out := raw.(map[string]any)
		if out["tag"] == "direct-wan" {
			// The controlled origin is loopback; production's protection stays on.
			out["settings"] = map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.1/32"}}}}
		}
	}
	for _, raw := range config["routing"].(map[string]any)["balancers"].([]any) {
		balancer := raw.(map[string]any)
		if balancer["tag"] == WANFallbackTag("main") {
			balancer["selector"] = []string{"direct-wan"}
		}
	}
	body, _ = json.Marshal(config)
	instance, err := core.StartInstance("json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	for _, tc := range []struct {
		name, domain string
		delay        time.Duration
		allowed      bool
	}{
		{"immediate", "example.com", 0, true},
		{"delayed", "example.com", 400 * time.Millisecond, true},
		{"slow client", "example.com", time.Second, true},
		{"busy client", "example.com", 3 * time.Second, true},
		{"strict delayed", "blocked.example.com", 400 * time.Millisecond, false},
		{"bounded unknown", "example.com", 5300 * time.Millisecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.TransparentPort), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(8 * time.Second))
			time.Sleep(tc.delay)
			// This local test checks routing independently of the peer's SAN.
			secured := tls.Client(conn, &tls.Config{ServerName: tc.domain, InsecureSkipVerify: true})
			err = secured.Handshake()
			if !tc.allowed {
				if err == nil {
					t.Fatal("strict or unclassified connection escaped through WAN")
				}
				return
			}
			if err != nil {
				t.Fatal("domain WAN rule lost delayed ClientHello", err)
			}
			if _, err = io.WriteString(secured, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(secured), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			got, err := io.ReadAll(response.Body)
			if err != nil || string(got) != "rule-wan-verified" {
				t.Fatalf("unexpected WAN response: %q %v", got, err)
			}
		})
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("origin accepted %d requests, want only four allowed domains", got)
	}
}
