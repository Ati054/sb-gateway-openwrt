package xray

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"github.com/xtls/xray-core/app/dispatcher"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"io"
	"net"
	"testing"
)

func TestManagedDNSFragmentedPrefixWaitsWithoutLosingOtherSniffers(t *testing.T) {
	root := context.WithValue(t.Context(), core.XrayKey(1), &core.Instance{})
	for _, tag := range []string{"resproxy-dns-transparent", "resproxy-dns-transparent6"} {
		ctx := session.ContextWithInbound(root, &session.Inbound{Tag: tag})
		q := gatewayDNSQuery()
		frame := append(binary.BigEndian.AppendUint16(nil, uint16(len(q))), q...)
		sniffer := dispatcher.NewSniffer(ctx)
		for _, end := range []int{1, 2, 4, 8, 13, len(frame) - 1} {
			if _, err := sniffer.Sniff(ctx, frame[:end], xnet.Network_TCP); err != protocol.ErrProtoNeedMoreData {
				t.Fatalf("prefix %d prematurely exhausted read attempts: %v", end, err)
			}
		}
		if result, err := sniffer.Sniff(ctx, frame, xnet.Network_TCP); err != nil || result.Protocol() != "dns" {
			t.Fatalf("fragmented DNS lost: %v", err)
		}
		// A TLS record can initially look like a bounded DNS length prefix.
		// Provisional DNS matching must retain TLS/HTTP classification.
		a, b := net.Pipe()
		client := tls.Client(a, &tls.Config{ServerName: "fragment.example.test"})
		done := make(chan struct{})
		go func() { defer close(done); _ = client.Handshake() }()
		var header [5]byte
		if _, err := io.ReadFull(b, header[:]); err != nil {
			t.Fatal(err)
		}
		hello := append([]byte(nil), header[:]...)
		body := make([]byte, int(binary.BigEndian.Uint16(header[3:])))
		if _, err := io.ReadFull(b, body); err != nil {
			t.Fatal(err)
		}
		hello = append(hello, body...)
		b.Close()
		client.Close()
		<-done
		sniffer = dispatcher.NewSniffer(ctx)
		for _, end := range []int{1, 2, 4} {
			sniffer.Sniff(ctx, hello[:end], xnet.Network_TCP)
		}
		if result, err := sniffer.Sniff(ctx, hello, xnet.Network_TCP); err != nil || result.Protocol() != "tls" || result.Domain() != "fragment.example.test" {
			t.Fatalf("provisional DNS swallowed TLS: %+v %v", result, err)
		}
	}
}

func gatewayDNSQuery() []byte {
	return []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 4, 't', 'e', 's', 't', 0, 0, 1, 0, 1}
}

func TestManagedDNSClassificationPreservesEmptyQueriesAndOtherProtocols(t *testing.T) {
	root := context.WithValue(context.Background(), core.XrayKey(1), &core.Instance{})
	q := gatewayDNSQuery()
	for _, stream := range []bool{false, true} {
		for _, p := range [][]byte{q, q[:12]} {
			p = append([]byte(nil), p...)
			if len(p) == 12 {
				p[5] = 0
			}
			if stream {
				p = append(binary.BigEndian.AppendUint16(nil, uint16(len(p))), p...)
			}
			ctx := session.ContextWithInbound(root, &session.Inbound{Tag: "resproxy-dns-transparent"})
			network := xnet.Network_UDP
			if stream {
				network = xnet.Network_TCP
			}
			result, err := dispatcher.NewSniffer(ctx).Sniff(ctx, p, network)
			if err != nil || result == nil || result.Protocol() != "dns" {
				t.Fatalf("managed DNS classification lost request: stream=%v %v", stream, err)
			}
		}
	}
	p := make([]byte, 1280)
	p[0], p[4], p[5], p[14] = 0xc3, 1, 8, 8
	ctx := session.ContextWithInbound(root, &session.Inbound{Tag: "resproxy-dns-transparent"})
	if result, _ := dispatcher.NewSniffer(ctx).Sniff(ctx, p, xnet.Network_UDP); result != nil && result.Protocol() == "dns" {
		t.Fatal("QUIC-shaped data was hijacked as DNS")
	}
	ctx = session.ContextWithInbound(root, &session.Inbound{Tag: "resproxy-transparent"})
	if result, _ := dispatcher.NewSniffer(ctx).Sniff(ctx, q, xnet.Network_UDP); result != nil && result.Protocol() == "dns" {
		t.Fatal("ordinary traffic was subjected to managed DNS classification")
	}
}

func TestManagedDNSRenderKeepsSourcePoliciesAndLocalRelay(t *testing.T) {
	c := DefaultBaseConfig()
	c.ManagedDNSPort = 1053
	c.Policies = []BasePolicy{{ID: "main"}, {ID: "office", SourceCIDRs: []string{"192.0.2.8/32"}, Rules: []RouteRule{{ID: "direct", Action: "direct", Ports: "53", Inbounds: []string{"transparent"}}}}}
	body, err := RenderBase(c)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	rules := config["routing"].(map[string]any)["rules"].([]any)
	idle := config["policy"].(map[string]any)["levels"].(map[string]any)["1"].(map[string]any)["connIdle"].(float64)
	if idle <= 5 || idle > 15 {
		t.Fatal("DNS relay closes before backend timeout replies or retains idle flows too long")
	}
	if rules[0].(map[string]any)["outboundTag"] != "resproxy-local-dns" {
		t.Fatal("DNS was routed before classification")
	}
	foundSource, foundException := false, false
	for _, raw := range rules {
		rule := raw.(map[string]any)
		encoded, _ := json.Marshal(rule)
		if rule["sourceIP"] != nil && bytes.Contains(encoded, []byte("resproxy-dns-transparent")) {
			foundSource = true
		}
		if rule["port"] == "53" && bytes.Contains(encoded, []byte("resproxy-dns-transparent")) {
			foundException = true
		}
	}
	if !foundSource || !foundException {
		t.Fatal("non-DNS traffic lost source-specific route conditions")
	}
}
