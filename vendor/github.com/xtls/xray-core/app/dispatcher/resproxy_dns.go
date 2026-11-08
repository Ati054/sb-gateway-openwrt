package dispatcher

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	dnsproto "github.com/xtls/xray-core/common/protocol/dns"
	"github.com/xtls/xray-core/common/session"
)

type managedDNSHeader struct{}

// A short length/header prefix is provisional: preserve TLS/HTTP sniffers
// while continuing within the existing shared sniffing deadline.
var errManagedDNSPrefix = errors.New("incomplete managed DNS prefix")

func (managedDNSHeader) Protocol() string { return "dns" }
func (managedDNSHeader) Domain() string   { return "" }

// These listeners are reached only by managed external port-53 capture. Keep
// ordinary transparent/SOCKS sniffing unchanged. A missing DNS signature falls
// through to normal HTTP/TLS/QUIC classification and the client's routing list.
func managedDNSSniffers(ctx context.Context) []protocolSnifferWithMetadata {
	in := session.InboundFromContext(ctx)
	if in == nil || (in.Tag != "resproxy-dns-transparent" && in.Tag != "resproxy-dns-transparent6") {
		return nil
	}
	return []protocolSnifferWithMetadata{
		{func(_ context.Context, p []byte) (SniffResult, error) { return sniffManagedDNS(p, false) }, false, net.Network_UDP},
		{func(_ context.Context, p []byte) (SniffResult, error) { return sniffManagedDNS(p, true) }, false, net.Network_TCP},
	}
}

func sniffManagedDNS(packet []byte, stream bool) (SniffResult, error) {
	length := len(packet)
	if stream {
		if len(packet) < 2 {
			return nil, errManagedDNSPrefix
		}
		length = int(binary.BigEndian.Uint16(packet[:2]))
		packet = packet[2:]
		if length < 12 || length > 8192 {
			return nil, errUnknownContent
		}
	}
	if len(packet) < 12 {
		if stream {
			return nil, errManagedDNSPrefix
		}
		return nil, errUnknownContent
	}
	// Recognize the DNS request envelope before parsing names. Small malformed
	// question counts still reach the DNS backend for its FORMERR response.
	if !dnsproto.IsRequestHeader(packet) {
		return nil, errUnknownContent
	}
	if stream && len(packet) < length {
		return nil, protocol.ErrProtoNeedMoreData
	}
	if stream {
		packet = packet[:length]
	}
	if !dnsproto.IsRequestPacket(packet) {
		return nil, errUnknownContent
	}
	return managedDNSHeader{}, nil
}
