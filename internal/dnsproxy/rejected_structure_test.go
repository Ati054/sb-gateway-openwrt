package dnsproxy

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestRejectedStructureDistinguishesDeclaredAndCompleteQuestions(t *testing.T) {
	q := dnsQuery("private.example.test")
	q[5] = 2
	got := inspectRejectedStructure(q).report()
	if got == nil || got.CompleteQuestions != 1 || got.Section != "question" || got.Problem != "name" {
		t.Fatalf("%+v", got)
	}
	second := dnsQuery("another.example.test")[12:]
	q = append(q, second...)
	got = inspectRejectedStructure(q).report()
	if got == nil || got.CompleteQuestions != 2 || got.Problem != "none" {
		t.Fatalf("%+v", got)
	}
}

func TestRejectedStructureExplainsTruncatedEDNSWithoutRetainingContents(t *testing.T) {
	q := dnsQuery("private.example.test")
	q[11] = 1
	// OPT declares four bytes, only two follow its header.
	q = append(q, 0, 0, 41, 4, 208, 0, 0, 0, 0, 0, 4, 0, 0)
	got := inspectRejectedStructure(q).report()
	if got == nil || got.Section != "additional" || got.Problem != "record_data" || got.RecordType != 41 || got.Required != 4 || got.Available != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestRejectedReportDistinguishesListenerFromTProxyOriginalTCP(t *testing.T) {
	d := &rejectedHeaders{}
	q := dnsQuery("private.example.test")
	q[5] = 2
	peer := netip.MustParseAddrPort("192.0.2.8:12345")
	d.recordPeer(q, peer, ingressTCP, queryQuestionCount)
	d.recordPeer(q, peer, ingressRelayTCP, queryQuestionCount)
	direct := d.entries[queryQuestionCount][ingressTCP].report()
	relay := d.entries[queryQuestionCount][ingressRelayTCP].report()
	if direct.Delivery != "listener" || direct.SocketTransport != "tcp" || relay.Delivery != "tproxy" || relay.SocketTransport != "udp" || relay.Transport != "tcp" || relay.Source != peer.Addr().String() {
		t.Fatalf("%+v %+v", direct, relay)
	}
	q[2] |= 0x80
	d.recordPeer(q, peer, ingressRelayTCP, queryResponsePacket)
	if d.entries[queryResponsePacket][ingressRelayTCP].report().Handling != "ignore_response" {
		t.Fatal("response classified as replyable")
	}
	binary.BigEndian.PutUint16(q[4:6], 60000)
	if got := inspectRejectedStructure(q).report(); got != nil {
		t.Fatal("random non-query data was interpreted as structured DNS")
	}
}
