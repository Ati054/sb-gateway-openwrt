//go:build linux

package capture

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"

	"golang.org/x/sys/unix"
)

func ctTestAttr(kind uint16, data []byte) []byte {
	n := 4 + len(data)
	b := make([]byte, (n+3)&^3)
	binary.NativeEndian.PutUint16(b, uint16(n))
	binary.NativeEndian.PutUint16(b[2:], kind)
	copy(b[4:], data)
	return b
}
func ctTestTuple(tuple ctTuple) []byte {
	ip := append(ctTestAttr(1, tuple.source.AsSlice()), ctTestAttr(2, tuple.target.AsSlice())...)
	if tuple.source.Is6() {
		ip = append(ctTestAttr(3, tuple.source.AsSlice()), ctTestAttr(4, tuple.target.AsSlice())...)
	}
	proto := ctTestAttr(1, []byte{tuple.protocol})
	proto = append(proto, ctTestAttr(2, binary.BigEndian.AppendUint16(nil, tuple.sport))...)
	proto = append(proto, ctTestAttr(3, binary.BigEndian.AppendUint16(nil, tuple.dport))...)
	return append(ctTestAttr(0x8001, ip), ctTestAttr(0x8002, proto)...)
}
func ctTestEntry(entry ctEntry) []byte {
	family := byte(unix.AF_INET)
	if entry.original.source.Is6() {
		family = unix.AF_INET6
	}
	p := []byte{family, 0, 0, 0}
	p = append(p, ctTestAttr(0x8001, ctTestTuple(entry.original))...)
	p = append(p, ctTestAttr(0x8002, ctTestTuple(entry.reply))...)
	p = append(p, ctTestAttr(3, binary.BigEndian.AppendUint32(nil, entry.status))...)
	if entry.identity {
		p = append(p, ctTestAttr(12, binary.BigEndian.AppendUint32(nil, 1234567))...)
	}
	return append(p, ctTestAttr(18, binary.BigEndian.AppendUint16(nil, 7))...)
}

func TestLegacyDNSCleanupScopeRetainsOtherConnections(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		for _, protocol := range []byte{6, 17} {
			client, router, remote, other := "192.0.2.8", "192.0.2.1", "203.0.113.53", "198.51.100.8"
			prefix := "192.0.2.0/24"
			if v6 {
				client, router, remote, other = "2001:db8:1::8", "2001:db8:1::1", "2001:db8:2::53", "2001:db8:3::8"
				prefix = "2001:db8:1::/64"
			}
			ip := netip.MustParseAddr
			scope := ctScope{port: 1053, local: map[netip.Addr]bool{ip(router): true}, ingress: map[netip.Addr]bool{ip(router): true}, clients: []netip.Prefix{netip.MustParsePrefix(prefix)}}
			original := ctEntry{original: ctTuple{ip(client), ip(remote), 41000, 53, protocol}, reply: ctTuple{ip(router), ip(client), 1053, 41000, protocol}, status: 1 << 5, identity: true}
			for name, change := range map[string]func(*ctEntry){
				"legacy":                  func(*ctEntry) {},
				"router-local-dns":        func(e *ctEntry) { e.original.target = ip(router) },
				"other-dns-service":       func(e *ctEntry) { e.reply.sport = 5053 },
				"ordinary-https":          func(e *ctEntry) { e.original.dport = 443 },
				"untranslated-dns":        func(e *ctEntry) { e.status = 0; e.reply.source = ip(remote); e.reply.sport = 53 },
				"other-ingress":           func(e *ctEntry) { e.reply.source = ip(other) },
				"other-client-network":    func(e *ctEntry) { e.original.source = ip(other); e.reply.target = ip(other) },
				"changed-reply-client":    func(e *ctEntry) { e.reply.target = ip(other) },
				"changed-reply-port":      func(e *ctEntry) { e.reply.dport = 41001 },
				"missing-kernel-identity": func(e *ctEntry) { e.identity = false },
			} {
				e := original
				change(&e)
				parsed, valid := ctReadEntry(ctTestEntry(e))
				match := valid && scope.matches(parsed)
				if match != (name == "legacy") {
					t.Fatalf("v6=%v proto=%d case=%s: cleanup scope was not exact", v6, protocol, name)
				}
				if name == "legacy" {
					attributes := map[uint16]int{}
					if !ctAttributes(parsed.deletion[4:], func(kind uint16, value, raw []byte) { attributes[kind]++ }) {
						t.Fatal("invalid deletion")
					}
					if len(attributes) != 3 || attributes[1] != 1 || attributes[12] != 1 || attributes[18] != 1 {
						t.Fatal("deletion lost original tuple, connection ID or zone")
					}
				}
			}
		}
	}
}

func TestConntrackParserRejectsTruncationAndKernelErrors(t *testing.T) {
	for _, p := range [][]byte{nil, {2}, {2, 0, 0, 0, 3, 0, 1, 0}, {2, 0, 0, 0, 20, 0, 1, 0}} {
		if _, ok := ctReadEntry(p); ok {
			t.Fatal("truncated conntrack record accepted")
		}
	}
	if ctNetlinkError([]byte{0, 0, 0, 0}) != nil {
		t.Fatal("successful ACK rejected")
	}
	code := -int32(unix.ENOENT)
	p := binary.NativeEndian.AppendUint32(nil, uint32(code))
	if !errors.Is(ctNetlinkError(p), unix.ENOENT) {
		t.Fatal("kernel errno lost")
	}
	if ctNetlinkError(nil) == nil {
		t.Fatal("truncated ACK accepted")
	}
}
