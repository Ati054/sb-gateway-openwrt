package dnsproxy

import (
	"encoding/binary"

	"golang.org/x/net/dns/dnsmessage"
)

// Diagnose bounds only after rejecting a DNS-shaped request. The authoritative
// validator is still dnsmessage.Parser. This retains no name, ID or raw bytes.
type rejectedStructure struct {
	section   uint8
	problem   uint8
	questions uint8
	offset    uint16
	required  uint16
	available uint16
	rrType    uint16
}

type rejectedStructureReport struct {
	Section           string `json:"section"`
	Problem           string `json:"problem"`
	CompleteQuestions uint8  `json:"complete_questions"`
	Offset            uint16 `json:"offset"`
	Required          uint16 `json:"required_bytes"`
	Available         uint16 `json:"available_bytes"`
	RecordType        uint16 `json:"record_type,omitempty"`
}

func (s rejectedStructure) report() *rejectedStructureReport {
	if s.section == 0 {
		return nil
	}
	return &rejectedStructureReport{Section: [...]string{"", "question", "answer", "authority", "additional"}[s.section], Problem: [...]string{"none", "name", "question_fields", "record_header", "record_data", "parser_rejected", "inspection_limit"}[s.problem], CompleteQuestions: s.questions, Offset: s.offset, Required: s.required, Available: s.available, RecordType: s.rrType}
}

func inspectRejectedStructure(message []byte) rejectedStructure {
	var result rejectedStructure
	if len(message) < 12 || message[2]&0xf8 != 0 || binary.BigEndian.Uint16(message[4:6]) > 8 {
		return result
	}
	offset := 12
	fail := func(section, problem uint8, need int) rejectedStructure {
		result.section, result.problem = section, problem
		result.offset = uint16(offset)
		result.required = uint16(need)
		result.available = uint16(max(0, len(message)-offset))
		return result
	}
	questions := int(binary.BigEndian.Uint16(message[4:6]))
	for range questions {
		next, ok := skipName(message, offset)
		if !ok {
			return fail(1, 1, 1)
		}
		offset = next
		if offset+4 > len(message) {
			return fail(1, 2, 4)
		}
		offset += 4
		result.questions++
	}
	steps := 0
	for section := uint8(2); section <= 4; section++ {
		count := int(binary.BigEndian.Uint16(message[2*int(section)+2:]))
		for range count {
			steps++
			if steps > 128 {
				return fail(section, 6, 0)
			}
			next, ok := skipName(message, offset)
			if !ok {
				return fail(section, 1, 1)
			}
			offset = next
			if offset+10 > len(message) {
				return fail(section, 3, 10)
			}
			result.rrType = binary.BigEndian.Uint16(message[offset:])
			size := int(binary.BigEndian.Uint16(message[offset+8:]))
			offset += 10
			if offset+size > len(message) {
				return fail(section, 4, size)
			}
			offset += size
		}
	}
	var parser dnsmessage.Parser
	_, err := parser.Start(message)
	if err == nil {
		for index, skip := range []func() error{parser.SkipAllQuestions, parser.SkipAllAnswers, parser.SkipAllAuthorities, parser.SkipAllAdditionals} {
			if skip() != nil {
				result.section = uint8(index + 1)
				result.problem = 5
				return result
			}
		}
	}
	result.section = 1
	return result
}
