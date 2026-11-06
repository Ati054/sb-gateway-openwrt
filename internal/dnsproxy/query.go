package dnsproxy

import "golang.org/x/net/dns/dnsmessage"

// Header-only and EDNS-only probes have no name to route on. Validate their
// sections without allocating records; the caller still applies the client's
// default route and its normal authorization before contacting an upstream.
func validQuestionlessMessage(message []byte) bool {
	if len(message) < 12 || len(message) > maxDNSMessage || message[4] != 0 || message[5] != 0 {
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
