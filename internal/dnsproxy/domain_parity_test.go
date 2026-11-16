package dnsproxy

import (
	"strings"
	"testing"

	"github.com/Ati054/xroute-openwrt/internal/xray"
	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common/geodata"
)

func TestCustomDomainRulesMatchTheActualXrayMatcher(t *testing.T) {
	for _, tc := range []struct {
		expression, host string
		want             bool
	}{
		{`regexp:^Google\.com$`, "google.com", false},
		{`regexp:(?i)^Google\.com$`, "google.com", true},
		{`regexp:(?-i)^Google\.com$`, "Google.com", false},
		{`regexp:^\D+\.com$`, "google.com", true},
		{`regexp:^\P{Greek}+\.com$`, "google.com", true},
		{"FULL:GOOGLE.COM", "Google.com", true},
		{"full:ЯНДЕКС.РФ", "xn--d1acpjx3f.xn--p1ai", true},
		{"domain:ЯНДЕКС.РФ", "www.xn--d1acpjx3f.xn--p1ai", true},
		{"domain:ЯНДЕКС.РФ", "other.xn--p1ai", false},
		{"keyword:GOOGLE", "www.google.com", true},
	} {
		expression, err := xray.NormalizeRuleDomain(tc.expression)
		if err != nil {
			t.Fatal(err)
		}
		dns, err := compileMatcher(Policy{DirectServiceExpressions: []string{expression}})
		if err != nil {
			t.Fatal(err)
		}
		rules, err := geodata.ParseDomainRules([]string{expression}, geodata.Domain_Domain)
		if err != nil {
			t.Fatal(err)
		}
		core, err := router.NewDomainMatcher(rules)
		if err != nil {
			t.Fatal(err)
		}
		dnsMatch, coreMatch := dns.matches(strings.ToLower(tc.host)), core.ApplyDomain(tc.host)
		if dnsMatch != tc.want || coreMatch != tc.want {
			t.Fatalf("%q host=%q DNS=%v core=%v want=%v", expression, tc.host, dnsMatch, coreMatch, tc.want)
		}
	}
}
