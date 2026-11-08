package session

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
)

// Managed gateway packages enable provenance with a linker override. Generic
// Xray builds keep it off; the environment switch supports controlled comparisons.
var dnsOriginDefault = "0"

func DNSOriginEnabled() bool {
	return dnsOriginDefault == "1" && os.Getenv("RESPROXY_DNS_ORIGIN") != "0"
}

// DNSOrigin is local diagnostic metadata, never an authentication claim.
// Stage 1 observes the accepted managed UDP buffer after read-only recognition,
// before transferring it. It does not observe kernel socket truncation flags;
// stage 2 observes one parsed TCP DNS frame before relay copying.
type DNSOrigin struct {
	Stage  byte
	Size   uint16
	Header [10]byte // DNS flags/counts only; excludes the transaction ID.
	Digest [16]byte
}

func NewDNSOrigin(payload []byte, stage byte) DNSOrigin {
	result := DNSOrigin{Stage: stage, Size: uint16(len(payload))}
	if len(payload) > 2 {
		copy(result.Header[:], payload[2:min(12, len(payload))])
	}
	sum := sha256.Sum256(payload)
	copy(result.Digest[:], sum[:16])
	return result
}

// Application-specific PROXY-v2 TLV, fixed layout/version. Its digest is
// transient on loopback and must not be published in diagnostics.
func (o DNSOrigin) TLV() [33]byte {
	var result [33]byte
	result[0], result[2], result[3], result[4] = 0xe0, 30, 1, o.Stage
	binary.BigEndian.PutUint16(result[5:7], o.Size)
	copy(result[7:17], o.Header[:])
	copy(result[17:], o.Digest[:])
	return result
}
