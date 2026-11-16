package xray

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuleIPsAcceptHostsAndCanonicalizeNetworks(t *testing.T) {
	for input, want := range map[string]string{"195.82.146.214": "195.82.146.214/32", "2001:db8::1": "2001:db8::1/128", "192.0.2.31/24": "192.0.2.0/24", "2001:db8::1234/64": "2001:db8::/64"} {
		got, err := NormalizeRuleIP(input)
		if err != nil || got != want {
			t.Fatal(input, got, err)
		}
	}
	for _, input := range []string{"999.1.1.1", "https://example.com", "192.0.2.1/33", "::ffff:192.0.2.1", "fe80::1%lan", "0.0.0.0/0", "::/0"} {
		if got, err := NormalizeRuleIP(input); err == nil {
			t.Fatal("invalid IP accepted", input, got)
		}
	}
}

func TestRuleInputValidationUsesBoundedNativeGrammar(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"domains":["full:ПРИМЕР.РФ","regexp:(?i)^Google\\.com$","keyword:google"],"cidrs":["195.82.146.214","2001:db8::1"]}`, true},
		{`{"domains":["https://example.com"]}`, false},
		{`{"domains":["regexp:(?=google)"]}`, false}, // JS accepts this; Go/Xray does not.
		{`{"domains":["regexp:(?P<label>google)\\.com$"]}`, true},
		{`{"domains":["keyword:яндекс"]}`, false},
		{`{"domains":["regexp:"]}`, false},
		{`{"cidrs":["192.0.2.1/99"]}`, false},
		{`{"domains":["example.com another.com"]}`, false},
	} {
		got, err := ValidateRuleInput(tc.body)
		if err != nil || got.Valid != tc.valid {
			t.Fatal(tc.body, got, err)
		}
	}
	if _, err := ValidateRuleInput(strings.Repeat(" ", 32769)); err == nil {
		t.Fatal("oversized request accepted")
	}
	result, err := ValidateRuleInput(`{"domains":["https://example.com"]}`)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(body), `"index":0`) {
		t.Fatal("first invalid entry lost its wire index", string(body), err)
	}
}
