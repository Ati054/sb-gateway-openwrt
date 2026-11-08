package dnsproxy

import (
	"crypto/sha256"
	"encoding/binary"
	"net"
	"net/netip"
)

const relayOriginTLVSize = 33
const maxDNSRelayHeader = 16 + 36 + relayOriginTLVSize

// Xray's framed reader and accepted UDP buffers are bounded at 8 KiB.
// Read one extra byte so the observer cannot silently truncate an allowed
// frame and mistake its own receive limit for corruption. The DNS work limit
// remains maxDNSMessage; larger queries receive REFUSED before admission.
const maxDNSRelayPayload = 8192

// This metadata verifies a local copy boundary, not a client's intent or
// identity. No digest, transaction ID, name or raw payload is retained.
type relayOrigin struct {
	stage       uint8
	size        uint16
	header      [10]byte
	match       bool
	checked     bool
	skip        uint8
	destination netip.AddrPort
}

type relayOriginReport struct {
	Stage         string  `json:"stage"`
	Integrity     string  `json:"integrity"`
	OriginalBytes uint16  `json:"original_bytes"`
	Destination   string  `json:"destination"`
	HeaderBytes   uint8   `json:"header_bytes"`
	Flags         *uint16 `json:"flags,omitempty"`
	Questions     *uint16 `json:"qdcount,omitempty"`
	Answers       *uint16 `json:"ancount,omitempty"`
	Authorities   *uint16 `json:"nscount,omitempty"`
	Additionals   *uint16 `json:"arcount,omitempty"`
}

func (o relayOrigin) report() *relayOriginReport {
	if o.stage == 0 {
		return nil
	}
	integrity := "not-checked-size"
	if o.skip == 2 {
		integrity = "receive-truncated"
	} else if o.checked {
		integrity = "mismatch"
		if o.match {
			integrity = "match"
		}
	}
	result := &relayOriginReport{Stage: [...]string{"", "xray-udp-buffer", "xray-dns-frame"}[o.stage], Integrity: integrity, OriginalBytes: o.size, Destination: o.destination.String(), HeaderBytes: uint8(min(o.size, 12))}
	fields := []**uint16{&result.Flags, &result.Questions, &result.Answers, &result.Authorities, &result.Additionals}
	for i, field := range fields {
		if int(o.size) >= 4+2*i {
			value := binary.BigEndian.Uint16(o.header[2*i : 2*i+2])
			*field = &value
		}
	}
	return result
}

func decodeRelayOrigin(tlv, payload []byte, destination netip.AddrPort) (relayOrigin, bool) {
	var result relayOrigin
	if len(tlv) != relayOriginTLVSize || tlv[0] != 0xe0 || binary.BigEndian.Uint16(tlv[1:3]) != 30 || tlv[3] != 1 || (tlv[4] != 1 && tlv[4] != 2) {
		return result, false
	}
	result.stage, result.size, result.destination = tlv[4], binary.BigEndian.Uint16(tlv[5:7]), destination
	copy(result.header[:], tlv[7:17])
	if int(result.size) > maxDNSMessage && len(payload) > maxDNSMessage {
		result.skip = 1
		return result, true
	}
	result.checked = true
	digest := sha256.Sum256(payload)
	result.match = int(result.size) == len(payload) && string(digest[:16]) == string(tlv[17:])
	// Include numeric header metadata in the consistency check too.
	var header [10]byte
	if len(payload) > 2 {
		copy(header[:], payload[2:min(12, len(payload))])
	}
	result.match = result.match && header == result.header
	return result, true
}

func relayResponse(query []byte, origin relayOrigin) bool {
	return (len(query) >= 3 && query[2]&0x80 != 0) || (origin.stage != 0 && origin.size >= 3 && origin.header[0]&0x80 != 0)
}

func readDNSDatagram(conn net.PacketConn, packet []byte) (int, net.Addr, bool, error) {
	if udp, ok := conn.(*net.UDPConn); ok {
		n, _, flags, peer, err := udp.ReadMsgUDP(packet, nil)
		return n, peer, dnsDatagramTruncated(flags), err
	}
	n, peer, err := conn.ReadFrom(packet)
	// PacketConn without flags cannot prove whether an exactly full buffer was
	// truncated. It must never produce an integrity mismatch verdict.
	return n, peer, n == len(packet), err
}
