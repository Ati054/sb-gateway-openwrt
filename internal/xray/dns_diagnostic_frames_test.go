package xray

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/dispatcher"
	xbuf "github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	dnsproto "github.com/xtls/xray-core/common/protocol/dns"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
)

// Reproduce only nonidentifying header/length characteristics from field
// diagnostics. Domains, addresses, source ports and payloads are not retained.
func diagnosticOpaqueFrames() [][]byte {
	var frames [][]byte
	for _, v := range [][6]int{{560, 32428, 29481, 41871, 50849, 55878}, {703, 31192, 61560, 63417, 23391, 45438}} {
		p := bytes.Repeat([]byte{0x5a}, v[0])
		binary.BigEndian.PutUint16(p[:2], 0x1234)
		for i, value := range v[1:] {
			binary.BigEndian.PutUint16(p[2+i*2:], uint16(value))
		}
		frames = append(frames, p)
	}
	return frames
}

func TestDiagnosticOpaqueFramesAreNotInitiallyClassifiedAsDNS(t *testing.T) {
	root := context.WithValue(t.Context(), core.XrayKey(1), &core.Instance{})
	ctx := session.ContextWithInbound(root, &session.Inbound{Tag: "resproxy-dns-transparent"})
	for _, body := range diagnosticOpaqueFrames() {
		for _, network := range []xnet.Network{xnet.Network_TCP, xnet.Network_UDP} {
			p := body
			if network == xnet.Network_TCP {
				p = append(binary.BigEndian.AppendUint16(nil, uint16(len(body))), body...)
			}
			result, _ := dispatcher.NewSniffer(ctx).Sniff(ctx, p, network)
			if result != nil && result.Protocol() == "dns" {
				t.Fatal("opaque first message hijacked as DNS")
			}
		}
	}
}

type diagnosticChunkReader struct {
	body  []byte
	chunk int
}

func (r *diagnosticChunkReader) Read(p []byte) (int, error) {
	if len(r.body) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.chunk, len(r.body))
	copy(p, r.body[:n])
	r.body = r.body[n:]
	return n, nil
}

func TestDNSFramingKeepsOpaqueAndValidMessagesExactAcrossFragmentation(t *testing.T) {
	messages := append([][]byte{gatewayDNSQuery()}, diagnosticOpaqueFrames()...)
	messages = append(messages, gatewayDNSQuery())
	var stream []byte
	for _, p := range messages {
		stream = binary.BigEndian.AppendUint16(stream, uint16(len(p)))
		stream = append(stream, p...)
	}
	for _, chunk := range []int{1, 2, 3, 13, 64, 8192} {
		reader := dnsproto.NewTCPReader(xbuf.NewReader(&diagnosticChunkReader{body: stream, chunk: chunk}))
		for _, want := range messages {
			got, err := reader.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			equal := bytes.Equal(got.Bytes(), want)
			got.Release()
			if !equal {
				t.Fatal("TCP DNS frame changed across buffers")
			}
		}
		if _, err := reader.ReadMessage(); err != io.EOF {
			t.Fatal(err)
		}
		reader.Close()
	}
}

func TestRealXrayOpaqueInitialTCP53PreservesBytesAndDNSStillRelays(t *testing.T) {
	binaryPath := testBinary(t)
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	backend, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	var dnsCount atomic.Int32
	go func() {
		packet := make([]byte, 65536)
		for {
			n, peer, err := backend.ReadFrom(packet)
			if err != nil {
				return
			}
			if n < 16 {
				continue
			}
			offset := 16 + int(binary.BigEndian.Uint16(packet[14:16]))
			if offset+12 > n {
				continue
			}
			reply := append([]byte(nil), packet[offset:n]...)
			reply[2] |= 0x80
			dnsCount.Add(1)
			backend.WriteTo(reply, peer)
		}
	}()
	base := DefaultBaseConfig()
	base.ManagedDNSPort = backend.LocalAddr().(*net.UDPAddr).Port
	body, err := RenderBase(base)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	config["inbounds"] = []any{map[string]any{"tag": "resproxy-dns-transparent", "listen": "127.0.0.1", "port": port, "protocol": "dokodemo-door", "settings": map[string]any{"address": "198.18.0.1", "port": 53, "network": "tcp"}, "sniffing": map[string]any{"enabled": true, "routeOnly": true, "destOverride": []string{"http", "tls", "quic"}}}}
	config["outbounds"] = append(config["outbounds"].([]any), map[string]any{"tag": "opaque-echo", "protocol": "freedom", "settings": map[string]any{"redirect": echo.Addr().String()}})
	config["routing"] = map[string]any{"domainStrategy": "AsIs", "rules": []any{map[string]any{"type": "field", "protocol": []string{"dns"}, "outboundTag": "resproxy-local-dns"}, map[string]any{"type": "field", "inboundTag": []string{"resproxy-dns-transparent"}, "outboundTag": "opaque-echo"}}}
	delete(config, "api")
	body, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "core.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(t.TempDir(), "core.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(binaryPath, "run", "-c", path)
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, e := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if e == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("core listener unavailable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	exchange := func(p []byte) []byte {
		t.Helper()
		c, e := net.DialTimeout("tcp", address, time.Second)
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(8 * time.Second))
		if _, e = c.Write(p); e != nil {
			t.Fatal(e)
		}
		reply := make([]byte, len(p))
		if _, e = io.ReadFull(c, reply); e != nil {
			t.Fatal(e)
		}
		return reply
	}
	q := gatewayDNSQuery()
	control := append(binary.BigEndian.AppendUint16(nil, uint16(len(q))), q...)
	want := append([]byte(nil), control...)
	want[4] |= 0x80
	if !bytes.Equal(exchange(control), want) || dnsCount.Load() != 1 {
		t.Fatal("DNS relay control was not observed")
	}
	for _, p := range diagnosticOpaqueFrames() {
		frame := append(binary.BigEndian.AppendUint16(nil, uint16(len(p))), p...)
		if !bytes.Equal(exchange(frame), frame) {
			t.Fatal("opaque TCP/53 bytes changed")
		}
	}
	if dnsCount.Load() != 1 {
		t.Fatal("opaque TCP/53 reached local DNS backend")
	}
}
