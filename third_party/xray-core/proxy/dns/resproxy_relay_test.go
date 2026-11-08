package dns

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"github.com/pires/go-proxyproto"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
)

type capturedRelay struct{ packets [][]byte }

func (w *capturedRelay) WriteMessage(b *buf.Buffer) error {
	w.packets = append(w.packets, append([]byte(nil), b.Bytes()...))
	b.Release()
	return nil
}
func relayPrefix(t *testing.T, udp bool) []byte {
	t.Helper()
	source := net.TCPDestination(net.IPAddress([]byte{192, 0, 2, 8}), 12345)
	target := net.TCPDestination(net.IPAddress([]byte{203, 0, 113, 53}), 53)
	if udp {
		source.Network = net.Network_UDP
		target.Network = net.Network_UDP
	}
	p, err := proxyproto.HeaderProxyFromAddrs(2, source.RawNetAddr(), target.RawNetAddr()).Format()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func writeRelay(t *testing.T, w *managedDNSWriter, p []byte) {
	t.Helper()
	b := buf.New()
	b.Write(p)
	if err := w.WriteMessage(b); err != nil {
		t.Fatal(err)
	}
}

func TestManagedDNSWriterTCPRefreshesEveryFrame(t *testing.T) {
	capture := &capturedRelay{}
	prefix := relayPrefix(t, false)
	writer := &managedDNSWriter{writer: capture, prefix: prefix}
	for _, p := range [][]byte{{}, {0x12}, {0x12, 0x34, 1}, bytes.Repeat([]byte{0x5a}, 36), bytes.Repeat([]byte{0x6b}, 42)} {
		writeRelay(t, writer, p)
	}
	for i, p := range capture.packets {
		body := p[61:]
		tlv := p[28:61]
		sum := sha256.Sum256(body)
		if tlv[4] != 2 || int(binary.BigEndian.Uint16(tlv[5:7])) != len(body) || !bytes.Equal(tlv[17:], sum[:16]) || binary.BigEndian.Uint16(p[14:16]) != 45 {
			t.Fatalf("stale TCP frame stamp: %d", i)
		}
	}
	if binary.BigEndian.Uint16(prefix[14:16]) != 12 {
		t.Fatal("cached address prefix mutated")
	}
}

func TestManagedDNSWriterUDPUsesAcceptedBufferStamp(t *testing.T) {
	first, second := bytes.Repeat([]byte{1}, 36), bytes.Repeat([]byte{2}, 36)
	capture := &capturedRelay{}
	for _, original := range [][]byte{first, second} {
		stamp := session.NewDNSOrigin(original, 1)
		writer := &managedDNSWriter{writer: capture, prefix: relayPrefix(t, true), origin: &stamp}
		// Simulate corruption after the accepted UDP buffer was observed.
		delivered := append([]byte(nil), original...)
		delivered[len(delivered)-1] ^= 1
		writeRelay(t, writer, delivered)
	}
	for i, p := range capture.packets {
		original := [][]byte{first, second}[i]
		sum := sha256.Sum256(original)
		deliveredSum := sha256.Sum256(p[61:])
		if p[32] != 1 || !bytes.Equal(p[45:61], sum[:16]) || bytes.Equal(p[45:61], deliveredSum[:16]) {
			t.Fatal("UDP input stamp was replaced or reused from another packet")
		}
	}
}

func TestManagedDNSWriterDisabledKeepsLegacyEnvelope(t *testing.T) {
	t.Setenv("RESPROXY_DNS_ORIGIN", "0")
	capture := &capturedRelay{}
	prefix := relayPrefix(t, false)
	writer := &managedDNSWriter{writer: capture, prefix: prefix}
	body := []byte{1, 2, 3}
	writeRelay(t, writer, body)
	if len(capture.packets) != 1 || !bytes.Equal(capture.packets[0], append(prefix, body...)) {
		t.Fatal("disabled diagnostics changed the relay envelope")
	}
}
