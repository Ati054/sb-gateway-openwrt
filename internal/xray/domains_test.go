package xray

import "testing"

func TestInternationalRuleDomainsNormalizeWithoutChangingExpressions(t *testing.T) {
	for input, expected := range map[string]string{
		"ПРИМЕР.РФ":                 "xn--e1afmkfd.xn--p1ai",
		"FULL:例子.中国":                "full:xn--fsqu00a.xn--fiqs8s",
		"Domain:EXAMPLE.COM.":       "domain:example.com",
		"DOMAIN:РФ":                 "domain:xn--p1ai",
		"REGEXP:(?i)Example\\.COM$": "regexp:(?i)Example\\.COM$",
		"KEYWORD:Пример":            "keyword:Пример",
	} {
		got, err := NormalizeRuleDomain(input)
		if err != nil || got != expected {
			t.Fatalf("%q => %q, want %q: %v", input, got, expected, err)
		}
	}
	for _, input := range []string{"https://пример.рф/path", "127.0.0.1", "bad..example", "full:-bad.example", "regexp:[", "unknown:example.com"} {
		if value, err := NormalizeRuleDomain(input); err == nil {
			t.Fatalf("invalid domain %q accepted as %q", input, value)
		}
	}
}
