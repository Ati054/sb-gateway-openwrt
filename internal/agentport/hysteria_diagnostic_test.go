//go:build linux

package agentport

import (
	"context"
	"encoding/json"
	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestIsolatedHysteriaSubscriptionDiagnostics(t *testing.T) {
	if os.Getenv("SB_DIAG_HYSTERIA") != "1" {
		t.Skip("opt-in real subscription diagnostic")
	}
	root := os.Getenv("SB_DIAG_ROOT")
	if root == "" {
		root = "/var/run/resproxy"
	}
	binary := os.Getenv("SB_TEST_XRAY")
	if binary == "" {
		binary = "/usr/libexec/resproxy/xray"
	}
	var source healthPool
	var state healthState
	live := map[string]any{}
	if readJSON(filepath.Join(root, "health-pool.json"), &source) != nil || readJSON(filepath.Join(root, "health-state/selector-health.json"), &state) != nil || readJSON(filepath.Join(root, "xray.json"), &live) != nil {
		t.Fatal("private runtime inventory unreadable")
	}
	metadata := map[string]healthNode{}
	for _, contract := range source.HealthPolicies {
		for id, n := range contract.Nodes {
			metadata[id] = n
		}
	}
	hy := []string{}
	allHy := 0
	controls := []string{}
	for id, n := range metadata {
		if n.Protocol == "hysteria2" {
			allHy++
			if os.Getenv("SB_DIAG_NO_HYSTERIA") == "1" {
				continue
			}
			if os.Getenv("SB_DIAG_REAL") == "1" || n.Country == "DE" || strings.Contains(n.Label, "\u0413\u0435\u0440\u043c\u0430\u043d\u0438\u044f") {
				hy = append(hy, id)
			}
		}
	}
	if filter := os.Getenv("SB_DIAG_LABEL_FILTER"); filter != "" {
		filtered := []string{}
		for _, id := range hy {
			if strings.Contains(metadata[id].Label, filter) {
				filtered = append(filtered, id)
			}
		}
		hy = filtered
	}
	sort.Strings(hy)
	t.Logf("INVENTORY hysteria_total=%d matching_nodes=%d", allHy, len(hy))
	if len(hy) == 0 && os.Getenv("SB_DIAG_REAL") != "1" {
		t.Fatal("matching nodes absent from applied pool")
	}
	if os.Getenv("SB_DIAG_REAL") == "1" {
		// Bound provider traffic while covering independent sources/transports.
		groups := map[string]int{}
		for _, id := range sortedKeys(metadata) {
			n := metadata[id]
			if transport := os.Getenv("SB_DIAG_VLESS_TRANSPORT"); transport != "" && n.Transport != transport {
				continue
			}
			if skip := os.Getenv("SB_DIAG_VLESS_SKIP_LABEL"); skip != "" && strings.Contains(n.Label, skip) {
				continue
			}
			if countries := os.Getenv("SB_DIAG_VLESS_COUNTRIES"); countries != "" && !strings.Contains(","+countries+",", ","+n.Country+",") {
				continue
			}
			key := n.SubscriptionID + "/" + n.Transport
			if n.Protocol == "vless" && groups[key] < 3 && len(controls) < 12 {
				controls = append(controls, id)
				groups[key]++
			}
		}
	} else {
		for _, id := range sortedKeys(metadata) {
			n := metadata[id]
			if n.Protocol != "vless" || n.Country != "DE" {
				continue
			}
			for _, s := range state {
				if s.AvailabilityOK[id] {
					controls = append(controls, id)
					break
				}
			}
			if len(controls) == 1 {
				break
			}
		}
	}
	if len(controls) == 0 && len(hy) == 0 {
		t.Fatal("no subscription nodes match diagnostic filters")
	}
	work := t.TempDir()
	pool := healthPool{Version: 4, BaseOutboundTags: []string{"diag-base"}, Policies: map[string][]string{}, Outbounds: map[string]json.RawMessage{}}
	pool.Outbounds["diag-direct"] = json.RawMessage(`{"tag":"diag-direct","protocol":"freedom"}`)
	nodes := append(append([]string{"diag-direct"}, controls...), hy...)
	for _, id := range nodes {
		if id != "diag-direct" {
			pool.Outbounds[id] = source.Outbounds[id]
			if metadata[id].Protocol == "hysteria2" && os.Getenv("SB_DIAG_QUIC_COMPAT") == "1" {
				var out map[string]any
				if json.Unmarshal(pool.Outbounds[id], &out) != nil {
					t.Fatal("private compatibility config invalid")
				}
				stream := out["streamSettings"].(map[string]any)
				mask, ok := stream["finalmask"].(map[string]any)
				if !ok {
					mask = map[string]any{}
					stream["finalmask"] = mask
				}
				mask["quicParams"] = map[string]any{"disableChromeParrot": true, "disableGSO": true, "disablePathMTUDiscovery": true}
				body, err := json.Marshal(out)
				if err != nil {
					t.Fatal("compatibility config encode failed")
				}
				pool.Outbounds[id] = body
			}
		}
	}
	port := func() int {
		l, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	api, proxy := port(), port()
	selector := "outbound-health-background"
	pool.Policies[selector] = nodes
	poolPath := filepath.Join(work, "pool.json")
	configPath := filepath.Join(work, "xray.json")
	logPath := filepath.Join(work, "core-private.log")
	if writeJSONAtomic(poolPath, pool) != nil {
		t.Fatal("private pool write failed")
	}
	config := map[string]any{"log": map[string]any{"loglevel": "debug", "error": logPath}, "dns": live["dns"], "policy": live["policy"], "api": map[string]any{"tag": "api", "services": []string{"RoutingService", "HandlerService"}}, "inbounds": []any{map[string]any{"tag": "api-in", "listen": "127.0.0.1", "port": api, "protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"}}, map[string]any{"tag": selector, "listen": "127.0.0.1", "port": proxy, "protocol": "http", "settings": map[string]any{}}}, "outbounds": []any{map[string]any{"tag": "diag-base", "protocol": "freedom"}}, "routing": map[string]any{"balancers": []any{map[string]any{"tag": selector, "selector": []string{"sb-health-" + shortHash(selector, 8) + "-"}, "strategy": map[string]any{"type": "random"}}}, "rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api-in"}, "outboundTag": "api"}, map[string]any{"type": "field", "inboundTag": []string{selector}, "balancerTag": selector}}}}
	if writeJSONAtomic(configPath, config) != nil {
		t.Fatal("private core config write failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	process := exec.CommandContext(ctx, binary, "run", "-config", configPath)
	if err := process.Start(); err != nil {
		t.Fatal("diagnostic core failed to start")
	}
	defer func() { process.Process.Kill(); process.Wait() }()
	for deadline := time.Now().Add(20 * time.Second); ; {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", api), 100*time.Millisecond)
		if e == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("diagnostic API unavailable")
		}
		time.Sleep(100 * time.Millisecond)
	}
	apiClient, err := xray.NewAPIClient(fmt.Sprintf("127.0.0.1:%d", api))
	if err != nil {
		t.Fatal("diagnostic persistent API unavailable")
	}
	defer apiClient.Close()
	probe := newXraySelectorRuntime(Options{XrayBinary: binary, XrayAPIServer: fmt.Sprintf("127.0.0.1:%d", api), RunXrayCommand: apiClient.Command, HealthPoolFile: poolPath, ProbeURL: fmt.Sprintf("http://127.0.0.1:%d", proxy)})
	probe.probeSelector = selector
	probe.probeContext = ctx
	if _, _, err := probe.Reload(); err != nil {
		t.Fatal("diagnostic runtime failed to load")
	}
	t.Logf("COMPAT_OPTIONS enabled=%t", os.Getenv("SB_DIAG_QUIC_COMPAT") == "1")
	if os.Getenv("SB_DIAG_UDP_COUNTERS") == "1" {
		if exec.Command("nft", "list", "table", "inet", "sbg_hy_diag").Run() == nil {
			t.Fatal("unowned counter table already exists")
		}
		diagnosticNft(t, "add", "table", "inet", "sbg_hy_diag")
		t.Cleanup(func() { exec.Command("nft", "delete", "table", "inet", "sbg_hy_diag").Run() })
		diagnosticNft(t, "add", "chain", "inet", "sbg_hy_diag", "out", "{ type filter hook output priority -310; policy accept; }")
		diagnosticNft(t, "add", "chain", "inet", "sbg_hy_diag", "in", "{ type filter hook input priority -310; policy accept; }")
	}
	logOffset := 0
	for _, id := range nodes {
		n := metadata[id]
		if id == "diag-direct" {
			n.Label = "direct-control"
			n.Protocol = "freedom"
		}
		if n.Protocol == "hysteria2" {
			var out struct {
				Protocol string `json:"protocol"`
				Settings struct {
					Version int `json:"version"`
					Port    int `json:"port"`
				} `json:"settings"`
				Stream struct {
					Network  string `json:"network"`
					Hysteria struct {
						Version int    `json:"version"`
						Auth    string `json:"auth"`
					} `json:"hysteriaSettings"`
					TLS struct {
						SNI      string   `json:"serverName"`
						ALPN     []string `json:"alpn"`
						Pins     string   `json:"pinnedPeerCertSha256"`
						Insecure bool     `json:"allowInsecure"`
					} `json:"tlsSettings"`
				} `json:"streamSettings"`
			}
			if json.Unmarshal(source.Outbounds[id], &out) != nil {
				t.Fatal("bad rendered outbound")
			}
			t.Logf("PARAMS label=%q protocol=%s version=%d transport=%s transport_version=%d auth_present=%t sni_present=%t explicit_alpn=%v pin_present=%t insecure=%t", n.Label, out.Protocol, out.Settings.Version, out.Stream.Network, out.Stream.Hysteria.Version, out.Stream.Hysteria.Auth != "", out.Stream.TLS.SNI != "", out.Stream.TLS.ALPN, out.Stream.TLS.Pins != "", out.Stream.TLS.Insecure)
		}
		counter := ""
		if n.Protocol == "hysteria2" && os.Getenv("SB_DIAG_UDP_COUNTERS") == "1" {
			var out struct {
				Settings struct {
					Address string `json:"address"`
					Port    int    `json:"port"`
				} `json:"settings"`
			}
			if json.Unmarshal(source.Outbounds[id], &out) != nil {
				t.Fatal("counter endpoint unreadable")
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", out.Settings.Address)
			if err != nil || len(ips) == 0 {
				t.Fatal("counter endpoint DNS failed")
			}
			ip := ips[0]
			family := "ip6"
			for _, value := range ips {
				if value.To4() != nil {
					ip = value
					family = "ip"
					break
				}
			}
			counter = "n" + shortHash(id, 8)
			for _, direction := range []string{"out", "in"} {
				diagnosticNft(t, "add", "counter", "inet", "sbg_hy_diag", counter+direction)
			}
			diagnosticNft(t, "add", "rule", "inet", "sbg_hy_diag", "out", family, "daddr", ip.String(), "udp", "dport", fmt.Sprint(out.Settings.Port), "counter", "name", counter+"out")
			diagnosticNft(t, "add", "rule", "inet", "sbg_hy_diag", "in", family, "saddr", ip.String(), "udp", "sport", fmt.Sprint(out.Settings.Port), "counter", "name", counter+"in")
		}
		start := time.Now()
		e := probe.Probe(id)
		if counter != "" {
			t.Logf("UDP_COUNTERS label=%q outgoing=%d incoming=%d scope=endpoint_including_live_background", n.Label, diagnosticCounter(t, counter+"out"), diagnosticCounter(t, counter+"in"))
		}
		t.Logf("PROBE label=%q protocol=%s provider=%q transport=%s ok=%t local_failure=%t failure=%s elapsed_seconds=%.2f targets=%v", n.Label, n.Protocol, n.SubscriptionID, n.Transport, e.OK, e.LocalFailure, e.Failure, time.Since(start).Seconds(), diagnosticTargets(e.Targets))
		body, _ := os.ReadFile(logPath)
		if logOffset > len(body) {
			logOffset = 0
		}
		delta := strings.ToLower(string(body[logOffset:]))
		logOffset = len(body)
		signals := []string{}
		for _, phrase := range []string{"no recent network activity", "timeout: handshake did not complete", "authentication failed", "authentication error", "auth failed", "invalid auth", "x509:", "certificate", "no application protocol", "no route to host", "network is unreachable", "connection refused", "failed to dial", "handshake"} {
			if strings.Contains(delta, phrase) {
				signals = append(signals, phrase)
			}
		}
		t.Logf("CORE_SIGNALS label=%q signals=%v", n.Label, signals)
		if os.Getenv("SB_DIAG_SAFE_ERRORS") == "1" {
			private := []string{}
			var collect func(any)
			collect = func(v any) {
				switch v := v.(type) {
				case string:
					if v != "" {
						private = append(private, v)
					}
				case map[string]any:
					for _, x := range v {
						collect(x)
					}
				case []any:
					for _, x := range v {
						collect(x)
					}
				}
			}
			for _, raw := range pool.Outbounds {
				var out any
				if json.Unmarshal(raw, &out) == nil {
					collect(out)
				}
			}
			sort.Slice(private, func(i, j int) bool { return len(private[i]) > len(private[j]) })
			lines := strings.Split(string(body), "\n")
			count := 0
			for i := len(lines) - 1; i >= 0 && count < 8; i-- {
				line := lines[i]
				lower := strings.ToLower(line)
				if !strings.Contains(lower, "failed") && !strings.Contains(lower, "error") && !strings.Contains(lower, "timeout") {
					continue
				}
				for _, value := range private {
					line = strings.ReplaceAll(line, value, "[redacted]")
				}
				if len(line) > 1600 {
					line = line[:1600]
				}
				t.Logf("SAFE_CORE_ERROR %s", line)
				count++
			}
		}
	}
}
func diagnosticTargets(targets map[string]*int) map[string]string {
	result := map[string]string{}
	for label, value := range targets {
		if value == nil {
			result[label] = "failed"
		} else {
			result[label] = "ok"
		}
	}
	return result
}

func diagnosticNft(t *testing.T, args ...string) {
	t.Helper()
	if err := exec.Command("nft", args...).Run(); err != nil {
		t.Fatal("private nft counter operation failed")
	}
}
func diagnosticCounter(t *testing.T, name string) uint64 {
	t.Helper()
	body, err := exec.Command("nft", "-j", "list", "counter", "inet", "sbg_hy_diag", name).Output()
	if err != nil {
		t.Fatal("counter read failed")
	}
	var value struct {
		Nftables []struct {
			Counter *struct {
				Packets uint64 `json:"packets"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	if json.Unmarshal(body, &value) != nil {
		t.Fatal("counter decode failed")
	}
	for _, item := range value.Nftables {
		if item.Counter != nil {
			return item.Counter.Packets
		}
	}
	t.Fatal("counter absent")
	return 0
}

// Checks persistence-to-runtime conversion without exposing values or changing the runtime.
func TestRenderedHysteriaMatchesCurrentCatalog(t *testing.T) {
	if os.Getenv("SB_DIAG_HYSTERIA") != "1" {
		t.Skip("opt-in private runtime comparison inside VM")
	}
	_, nodes, e := (generation.Store{Root: "/etc/resproxy"}).Current()
	if e != nil {
		t.Fatal("current catalog unreadable")
	}
	var p healthPool
	if readJSON("/var/run/resproxy/health-pool.json", &p) != nil {
		t.Fatal("runtime pool unreadable")
	}
	checked := 0
	for _, n := range nodes {
		if n.Protocol != "hysteria2" {
			continue
		}
		body, ok := p.Outbounds[n.ID]
		if !ok {
			continue
		}
		var o struct {
			Settings struct {
				Address string
				Port    int
			}
			Stream struct {
				Hysteria struct{ Auth string } `json:"hysteriaSettings"`
				TLS      struct {
					SNI string `json:"serverName"`
				} `json:"tlsSettings"`
			} `json:"streamSettings"`
		}
		if json.Unmarshal(body, &o) != nil {
			t.Fatal("runtime outbound invalid")
		}
		tls, _ := n.Settings["tls"].(map[string]any)
		sni, _ := tls["server_name"].(string)
		if n.Server != o.Settings.Address || n.Port != o.Settings.Port || n.Credentials["password"] != o.Stream.Hysteria.Auth || sni != o.Stream.TLS.SNI {
			t.Fatal("persisted and rendered endpoint/auth/SNI mismatch")
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no Hysteria2 nodes compared")
	}
	t.Logf("CATALOG_TO_RUNTIME hysteria_nodes=%d endpoint_auth_sni_match=true", checked)
}
