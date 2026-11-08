package dnsproxy

import (
	"bytes"
	"encoding/binary"
	"net/netip"
)

var relaySignature = []byte("\r\n\r\n\x00\r\nQUIT\n")

func hasRelaySignature(packet []byte) bool {
	return len(packet) >= 12 && bytes.Equal(packet[:12], relaySignature)
}

// This is a deliberately bounded subset of PROXY-v2: IP addresses, original
// TCP/UDP transport and one fixed local provenance TLV. The caller checks loopback first.
// LAN peers can never supply their own client identity through this envelope.
func unwrapDNSRelay(packet []byte) ([]byte, netip.AddrPort, dnsIngress, bool) {
	body, peer, ingress, _, ok := unwrapDNSRelayOrigin(packet)
	return body, peer, ingress, ok
}

func unwrapDNSRelayOrigin(packet []byte) ([]byte, netip.AddrPort, dnsIngress, relayOrigin, bool) {
	invalid := func() ([]byte, netip.AddrPort, dnsIngress, relayOrigin, bool) {
		return nil, netip.AddrPort{}, ingressUDP, relayOrigin{}, false
	}
	if len(packet) < 16 || !hasRelaySignature(packet) || packet[12] != 0x21 {
		return invalid()
	}
	size := int(binary.BigEndian.Uint16(packet[14:16]))
	addressSize := 0
	switch packet[13] >> 4 {
	case 1:
		addressSize = 12
	case 2:
		addressSize = 36
	default:
		return invalid()
	}
	// Validate the envelope independently of its DNS payload. A complete,
	// trusted envelope can carry a short DNS probe: the query validator must
	// answer FORMERR while retaining the original client and transport.
	if (size != addressSize && size != addressSize+relayOriginTLVSize) || len(packet) < 16+size || len(packet) > maxDNSRelayPayload+maxDNSRelayHeader+1 {
		return invalid()
	}
	ingress := ingressUDP
	if packet[13]&15 == 1 {
		ingress = ingressTCP
	} else if packet[13]&15 != 2 {
		return invalid()
	}
	var source netip.Addr
	var destination netip.Addr
	var port uint16
	var destinationPort uint16
	switch {
	case addressSize == 12:
		source = netip.AddrFrom4([4]byte(packet[16:20]))
		destination = netip.AddrFrom4([4]byte(packet[20:24]))
		port = binary.BigEndian.Uint16(packet[24:26])
		destinationPort = binary.BigEndian.Uint16(packet[26:28])
	case addressSize == 36:
		source = netip.AddrFrom16([16]byte(packet[16:32]))
		destination = netip.AddrFrom16([16]byte(packet[32:48]))
		port = binary.BigEndian.Uint16(packet[48:50])
		destinationPort = binary.BigEndian.Uint16(packet[50:52])
	default:
		return invalid()
	}
	if !source.IsValid() || source.IsUnspecified() || port == 0 {
		return invalid()
	}
	body := packet[16+size:]
	var origin relayOrigin
	if size != addressSize {
		var valid bool
		origin, valid = decodeRelayOrigin(packet[16+addressSize:16+size], body, netip.AddrPortFrom(destination.Unmap(), destinationPort))
		if !valid || (origin.stage == 1 && ingress != ingressUDP) || (origin.stage == 2 && ingress != ingressTCP) {
			return invalid()
		}
	}
	return body, netip.AddrPortFrom(source.Unmap(), port), ingress, origin, true
}
