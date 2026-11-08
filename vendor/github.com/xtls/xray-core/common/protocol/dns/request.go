package dns

import (
	"encoding/binary"
	"golang.org/x/net/dns/dnsmessage"
)

// IsRequestHeader recognizes the query envelope used by the managed DNS
// listeners. Small malformed question counts still reach the DNS backend and
// receive FORMERR; arbitrary UDP data is left to ordinary proxy routing.
func IsRequestHeader(p []byte) bool {
	return len(p) >= 12 && p[2]&0xf8 == 0 && p[3]&0x8f == 0 && binary.BigEndian.Uint16(p[4:6]) <= 8 &&
		binary.BigEndian.Uint16(p[6:8]) == 0 && binary.BigEndian.Uint16(p[8:10]) <= 1 && binary.BigEndian.Uint16(p[10:12]) <= 64
}

// IsRequestPacket additionally requires a complete first question, if present.
func IsRequestPacket(p []byte) bool {
	if !IsRequestHeader(p) {
		return false
	}
	if binary.BigEndian.Uint16(p[4:6]) == 0 {
		return true
	}
	var parser dnsmessage.Parser
	if _, err := parser.Start(p); err != nil {
		return false
	}
	_, err := parser.Question()
	return err == nil
}
