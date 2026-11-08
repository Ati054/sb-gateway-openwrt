package dnsproxy

import (
	"encoding/binary"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

// A fixed set of counters describes rejected requests without retaining client
// addresses, names, IDs, or packet contents. It cannot grow with traffic.
type queryFailure uint8

const (
	queryValid queryFailure = iota
	queryShortHeader
	queryResponsePacket
	queryQuestionCount
	queryQuestionlessSections
	queryTruncatedQuestion
	queryCompressedName
	queryInvalidLabel
	queryTruncatedName
	queryTooManyLabels
	queryTruncatedTypeClass
	queryInvalidSections
	queryRelayHeader
	queryRelayPayloadChanged
	queryTooLarge
	queryFailureCount
)

func (reason queryFailure) String() string {
	return [...]string{
		"valid", "short_header", "response_packet", "question_count",
		"questionless_sections", "truncated_question", "compressed_name",
		"invalid_label", "truncated_name", "too_many_labels", "truncated_type_class",
		"invalid_sections", "invalid_relay_header", "relay_payload_changed", "query_too_large",
	}[reason]
}

func parseQuestionName(query []byte) (string, queryFailure) {
	if len(query) >= 3 && query[2]&0x80 != 0 {
		return "", queryResponsePacket
	}
	if len(query) < 12 {
		return "", queryShortHeader
	}
	if query[2]&0x80 != 0 {
		return "", queryResponsePacket
	}
	if len(query) > maxDNSMessage {
		return "", queryTooLarge
	}
	switch binary.BigEndian.Uint16(query[4:6]) {
	case 0:
		if !validQuestionlessMessage(query) {
			return "", queryQuestionlessSections
		}
		return "", queryValid
	case 1:
	default:
		return "", queryQuestionCount
	}
	if len(query) < 17 {
		return "", queryTruncatedQuestion
	}
	labels := []string{}
	for offset := 12; offset < len(query); {
		length := int(query[offset])
		offset++
		if length == 0 {
			if offset+4 > len(query) {
				return "", queryTruncatedTypeClass
			}
			if !validDNSSections(query) {
				return "", queryInvalidSections
			}
			return strings.ToLower(strings.Join(labels, ".")), queryValid
		}
		if length&0xc0 == 0xc0 {
			return "", queryCompressedName
		}
		if length > 63 {
			return "", queryInvalidLabel
		}
		if offset+length > len(query) {
			return "", queryTruncatedName
		}
		if len(labels) >= 127 {
			return "", queryTooManyLabels
		}
		labels = append(labels, string(query[offset:offset+length]))
		offset += length
	}
	return "", queryTruncatedName
}

// Header-only and EDNS-only probes have no name to route on. Validate their
// sections without allocating records; the caller still applies the client's
// default route and its normal authorization before contacting an upstream.
func validQuestionlessMessage(message []byte) bool {
	if len(message) < 12 || len(message) > maxDNSMessage || message[4] != 0 || message[5] != 0 {
		return false
	}
	return validDNSSections(message)
}

func validDNSSections(message []byte) bool {
	if len(message) < 12 || len(message) > maxDNSMessage {
		return false
	}
	var parser dnsmessage.Parser
	if _, err := parser.Start(message); err != nil {
		return false
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return false
	}
	if err := parser.SkipAllAnswers(); err != nil {
		return false
	}
	if err := parser.SkipAllAuthorities(); err != nil {
		return false
	}
	return parser.SkipAllAdditionals() == nil
}
