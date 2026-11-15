package dnsproxy

import "testing"

func TestDNSQuestionMatchingUsesASCIICaseOnly(t *testing.T) {
	for _, tc := range []struct {
		name, query, reply string
		match              bool
	}{
		{"ascii", "MiXeD.Example", "mixed.EXAMPLE", true},
		{"latin-unicode-fold", "\u00c5.example", "\u00e5.example", false},
		{"kelvin-unicode-fold", "K.example", "\u212a.example", false},
		{"latin-octets", "\xdd.example", "\xfd.example", false},
		{"invalid-utf8-octets", "\x80.example", "\x81.example", false},
		{"same-high-octet-ascii-fold", "\xddA.example", "\xdda.EXAMPLE", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, response := dnsQuery(tc.query), dnsQuery(tc.reply)
			response[2] |= 0x80
			if got := matchingQuestion(query, response); got != tc.match {
				t.Fatalf("full response match=%v, want %v", got, tc.match)
			}
			response[2] |= 0x02
			if got := matchingTruncatedDNSResponse(query, response); got != tc.match {
				t.Fatalf("TC response match=%v, want %v", got, tc.match)
			}
		})
	}
}
