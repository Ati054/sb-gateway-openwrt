package dnsproxy

import (
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type dnsIngress uint8

const (
	ingressInternal dnsIngress = iota
	ingressUDP
	ingressTCP
	ingressRelayUDP
	ingressRelayTCP
	ingressCount
)

// Keep one latest numeric header for each reason and ingress. The fixed array
// contains no request bytes, names, transaction IDs, or client strings. Normal
// queries never take this lock; malformed bursts cannot grow its storage.
type rejectedHeader struct {
	sequence    uint64
	at          int64
	source      [16]byte
	sourceValid bool
	sourcePort  uint16
	wireHint    uint8
	reason      queryFailure
	ingress     dnsIngress
	size        uint16
	headerBytes uint8
	flags       uint16
	questions   uint16
	answers     uint16
	authorities uint16
	additionals uint16
	structure   rejectedStructure
	origin      relayOrigin
	response    bool
}

type rejectedHeaders struct {
	mu        sync.Mutex
	sequence  uint64
	written   uint64
	published bool
	entries   [queryFailureCount][ingressCount]rejectedHeader
	recent    [256]rejectedHeader
	hints     [3]rejectedHeader
}

func (d *rejectedHeaders) record(query []byte, source netip.Addr, ingress dnsIngress, reason queryFailure) {
	d.recordPeer(query, netip.AddrPortFrom(source, 0), ingress, reason)
}

func (d *rejectedHeaders) recordPeer(query []byte, peer netip.AddrPort, ingress dnsIngress, reason queryFailure, origins ...relayOrigin) {
	source := peer.Addr()
	entry := rejectedHeader{at: time.Now().UnixNano(), size: uint16(len(query)), headerBytes: uint8(min(len(query), 12)), sourcePort: peer.Port(), reason: reason, ingress: ingress, wireHint: rejectedWireHint(query, ingress)}
	if len(origins) > 0 {
		entry.origin = origins[0]
	}
	entry.response = relayResponse(query, entry.origin)
	if source.IsValid() {
		entry.source, entry.sourceValid = source.Unmap().As16(), true
	}
	if len(query) >= 4 {
		entry.flags = binary.BigEndian.Uint16(query[2:4])
	}
	if len(query) >= 6 {
		entry.questions = binary.BigEndian.Uint16(query[4:6])
	}
	if len(query) >= 8 {
		entry.answers = binary.BigEndian.Uint16(query[6:8])
	}
	if len(query) >= 10 {
		entry.authorities = binary.BigEndian.Uint16(query[8:10])
	}
	if len(query) >= 12 {
		entry.additionals = binary.BigEndian.Uint16(query[10:12])
	}
	if reason == queryQuestionCount || reason == queryInvalidSections || reason == queryQuestionlessSections {
		entry.structure = inspectRejectedStructure(query)
	}
	d.mu.Lock()
	d.sequence++
	entry.sequence = d.sequence
	d.entries[reason][ingress] = entry
	d.recent[(entry.sequence-1)%uint64(len(d.recent))] = entry
	if entry.wireHint != 0 {
		d.hints[entry.wireHint] = entry
	}
	d.mu.Unlock()
}

// A structural hint is not protocol identification: no decryption or peer
// authentication is possible here. Never retain a raw prefix, DNS ID or name.
// Short headers and unknown QUIC versions deliberately remain unidentified.
func rejectedWireHint(packet []byte, ingress dnsIngress) uint8 {
	if (ingress != ingressUDP && ingress != ingressRelayUDP) || len(packet) < 1200 || packet[0]&0xc0 != 0xc0 {
		return 0
	}
	version := binary.BigEndian.Uint32(packet[1:5])
	if version != 1 && version != 0x6b3343cf {
		return 0
	}
	dcid := int(packet[5])
	if dcid > 20 || 6+dcid >= len(packet) {
		return 0
	}
	scid := int(packet[6+dcid])
	if scid > 20 || 7+dcid+scid >= len(packet) {
		return 0
	}
	if version == 1 {
		return 1
	}
	return 2
}

type rejectedHeaderReport struct {
	Sequence        uint64                   `json:"sequence"`
	At              string                   `json:"at"`
	Reason          string                   `json:"reason"`
	Transport       string                   `json:"transport"`
	Delivery        string                   `json:"delivery"`
	SocketTransport string                   `json:"socket_transport"`
	Handling        string                   `json:"handling"`
	Structure       *rejectedStructureReport `json:"structure,omitempty"`
	Origin          *relayOriginReport       `json:"origin,omitempty"`
	Source          string                   `json:"source"`
	SourcePort      uint16                   `json:"source_port"`
	WireHint        string                   `json:"wire_hint,omitempty"`
	Bytes           uint16                   `json:"bytes"`
	HeaderBytes     uint8                    `json:"header_bytes"`
	Flags           uint16                   `json:"flags"`
	Opcode          uint16                   `json:"opcode"`
	Questions       uint16                   `json:"qdcount"`
	Answers         uint16                   `json:"ancount"`
	Authorities     uint16                   `json:"nscount"`
	Additionals     uint16                   `json:"arcount"`
}

func (entry rejectedHeader) report() rejectedHeaderReport {
	source := "unknown"
	if entry.sourceValid {
		source = netip.AddrFrom16(entry.source).Unmap().String()
	}
	handling := "formerr"
	if entry.reason == queryResponsePacket {
		handling = "ignore_response"
	}
	if entry.reason == queryRelayHeader {
		handling = "ignore_invalid_relay"
	}
	if entry.reason == queryRelayPayloadChanged {
		handling = "servfail_relay"
		if entry.response {
			handling = "ignore_corrupt_response"
		}
	}
	if entry.reason == queryTooLarge {
		handling = "refused_size"
		if entry.response {
			handling = "ignore_size_response"
		}
	}
	return rejectedHeaderReport{
		Sequence: entry.sequence, At: time.Unix(0, entry.at).UTC().Format(time.RFC3339Nano),
		Reason: entry.reason.String(), Transport: [...]string{"internal", "udp", "tcp", "udp", "tcp"}[entry.ingress], Source: source,
		Delivery: [...]string{"internal", "listener", "listener", "tproxy", "tproxy"}[entry.ingress], Handling: handling, Structure: entry.structure.report(), Origin: entry.origin.report(),
		SocketTransport: [...]string{"internal", "udp", "tcp", "udp", "udp"}[entry.ingress],
		SourcePort:      entry.sourcePort, WireHint: [...]string{"", "possible-quic-v1-long-header", "possible-quic-v2-long-header"}[entry.wireHint],
		Bytes: entry.size, HeaderBytes: entry.headerBytes, Flags: entry.flags, Opcode: entry.flags >> 11 & 15,
		Questions: entry.questions, Answers: entry.answers, Authorities: entry.authorities, Additionals: entry.additionals,
	}
}

func (s *server) rejectedHeadersPath() string {
	return filepath.Join(filepath.Dir(s.config.StatusPath), "dns-rejected.json")
}

func (s *server) writeRejectedHeaders() error {
	if s.config.StatusPath == "" {
		return nil
	}
	d := &s.rejectedHeaders
	d.mu.Lock()
	if d.published && d.written == d.sequence {
		d.mu.Unlock()
		return nil
	}
	sequence, entries, recent, hints := d.sequence, d.entries, d.recent, d.hints
	d.mu.Unlock()
	report := struct {
		Capacity        int                    `json:"capacity"`
		Samples         []rejectedHeaderReport `json:"samples"`
		RecentCapacity  int                    `json:"recent_capacity"`
		RecentSamples   []rejectedHeaderReport `json:"recent_samples"`
		WireHintSamples []rejectedHeaderReport `json:"wire_hint_samples"`
	}{Capacity: int(queryFailureCount-1) * int(ingressCount), Samples: []rejectedHeaderReport{}, RecentCapacity: len(recent), RecentSamples: []rejectedHeaderReport{}, WireHintSamples: []rejectedHeaderReport{}}
	for reason := queryFailure(1); reason < queryFailureCount; reason++ {
		for ingress := dnsIngress(0); ingress < ingressCount; ingress++ {
			entry := entries[reason][ingress]
			if entry.sequence == 0 {
				continue
			}
			report.Samples = append(report.Samples, entry.report())
		}
	}
	first := uint64(1)
	if sequence > uint64(len(recent)) {
		first = sequence - uint64(len(recent)) + 1
	}
	for n := first; n <= sequence; n++ {
		report.RecentSamples = append(report.RecentSamples, recent[(n-1)%uint64(len(recent))].report())
	}
	for _, entry := range hints {
		if entry.sequence != 0 {
			report.WireHintSamples = append(report.WireHintSamples, entry.report())
		}
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	// Publish at the regular status cadence and only when a rejection changed.
	// Never write from a listener or hold its diagnostic lock during disk I/O.
	file, err := os.CreateTemp(filepath.Dir(s.config.StatusPath), ".dns-rejected-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), s.rejectedHeadersPath()); err != nil {
		return err
	}
	d.mu.Lock()
	d.written, d.published = sequence, true
	d.mu.Unlock()
	return nil
}
