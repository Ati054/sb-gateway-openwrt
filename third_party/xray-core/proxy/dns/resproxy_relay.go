package dns

import (
	"encoding/binary"
	"github.com/xtls/xray-core/common/buf"
	dnsproto "github.com/xtls/xray-core/common/protocol/dns"
	"github.com/xtls/xray-core/common/session"
)

// The loopback-only backend receives a standard PROXY-v2 envelope per UDP
// message, retaining the actual client's address and transport after capture.
type managedDNSWriter struct {
	writer dnsproto.MessageWriter
	prefix []byte
	origin *session.DNSOrigin
}

func (w *managedDNSWriter) WriteMessage(b *buf.Buffer) error {
	if !session.DNSOriginEnabled() {
		packet := buf.NewWithSize(int32(len(w.prefix)) + b.Len())
		packet.Write(w.prefix)
		packet.Write(b.Bytes())
		b.Release()
		return w.writer.WriteMessage(packet)
	}
	var origin session.DNSOrigin
	if w.origin != nil {
		origin = *w.origin
	} else {
		origin = session.NewDNSOrigin(b.Bytes(), 2)
	}
	tlv := origin.TLV()
	packet := buf.NewWithSize(int32(len(w.prefix)+len(tlv)) + b.Len())
	packet.Write(w.prefix)
	binary.BigEndian.PutUint16(packet.Bytes()[14:16], uint16(len(w.prefix)-16+len(tlv)))
	packet.Write(tlv[:])
	packet.Write(b.Bytes())
	b.Release()
	return w.writer.WriteMessage(packet)
}
