package dnsproxy

import (
	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
	"testing"
)

func TestSharedDomainAssetPreservesMatchKindsAndRuleOrder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XRAY_LOCATION_ASSET", root)
	ref, err := geoipasset.PublishDomains(root, "cn", []string{"domain:example.cn", "full:exact.test", "keyword:needle", "regexp:^regex\\.test$"})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"direct", "proxy"} {
		m, err := compileMatcher(Policy{Rules: []Rule{{Action: action, Domains: []string{ref}}, {Action: "direct", Domains: []string{"domain:unrelated.test"}}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"example.cn", "www.example.cn", "exact.test", "a-needle.test", "regex.test"} {
			if m.matches(name) != (action == "direct") {
				t.Fatal(action, name)
			}
		}
		if m.matches("other.example") {
			t.Fatal("unrelated domain matched")
		}
		if !m.matches("unrelated.test") {
			t.Fatal("subsequent rule lost")
		}
	}
}
