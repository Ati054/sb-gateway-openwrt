package dnsproxy

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
)

func TestCompactDomainsMatchLegacyMaps(t *testing.T) {
	p := Policy{RUZoneMode: "both", DirectDomains: []string{"ROOT.TEST", ".dot.test", ""},
		DirectServiceExpressions: []string{"full:EXACT.TEST", "domain:suffix.test", "keyword:needle", `regexp:^custom[0-9]+\.test$`}}
	for i := 0; i < 4096; i++ {
		kind := "domain:"
		if i%5 == 0 {
			kind = "full:"
		}
		name := fmt.Sprintf("d%d.p%d.test", i, i%37)
		p.DirectServiceExpressions = append(p.DirectServiceExpressions, kind+name)
		if i%11 == 0 {
			p.DirectServiceExpressions = append(p.DirectServiceExpressions, "full:"+strings.ToUpper(name))
		}
	}
	compact, err := compileMatcher(p)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := compileMatcherWithIndex(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if compact.compact == nil || len(compact.exact) != 0 || len(compact.suffixes) != 2 {
		t.Fatal("custom names were not compacted, or special map suffixes changed")
	}
	names := []string{"root.test", "a.root.test", "notroot.test", "exact.test", "a.exact.test", "suffix.test", "a.suffix.test", "a-needle.test", "custom12.test", "customx.test", "site.ru", "site.xn--p1ai", "a.dot.test", ".dot.test", "a..dot.test", "", "root.test."}
	r := rand.New(rand.NewPCG(11, 22))
	for i := 0; i < 30000; i++ {
		n := r.IntN(6000)
		name := fmt.Sprintf("d%d.p%d.test", n, n%37)
		if r.IntN(2) == 0 {
			name = "www." + name
		}
		names = append(names, name)
	}
	for _, name := range names {
		if got, want := compact.matches(name), legacy.matches(name); got != want {
			t.Fatalf("%q: compact %v, maps %v", name, got, want)
		}
	}
	if n := testing.AllocsPerRun(100, func() { compact.matches("www.d42.p5.test") }); n != 0 {
		t.Fatalf("compact lookup allocates %v times", n)
	}
}

func TestCompactDomainsPreserveAssetsOrderAndOutage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XRAY_LOCATION_ASSET", root)
	ref, err := geoipasset.PublishDomains(root, "shared", []string{"domain:asset.test", "full:asset-exact.test"})
	if err != nil {
		t.Fatal(err)
	}
	large := []string{"domain:root.test", ref}
	for i := 0; i < compactDomainThreshold; i++ {
		large = append(large, fmt.Sprintf("domain:filler%d.test", i))
	}
	p := Policy{ProxyOutageAction: "block", Rules: []Rule{
		{Action: "proxy", OutageAction: "block", Domains: []string{"full:private.root.test"}},
		{Action: "proxy", OutageAction: "wan", Domains: large},
		{Action: "direct", Domains: []string{"domain:test"}},
	}}
	compact, err := compileMatcher(p)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := compileMatcherWithIndex(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if compact.ordered[1].matcher.compact == nil {
		t.Fatal("ordered custom rule did not get a compact index")
	}
	a := &server{stateValid: true, matchers: map[string]domainMatcher{"pc": compact}, ruleWANActive: map[string]bool{"resproxy-pc": true}}
	b := &server{stateValid: true, matchers: map[string]domainMatcher{"pc": legacy}, ruleWANActive: map[string]bool{"resproxy-pc": true}}
	for _, name := range []string{"private.root.test", "www.root.test", "www.asset.test", "asset-exact.test", "www.asset-exact.test", "other.test", "other.invalid"} {
		if a.matchesDirect("pc", name) != b.matchesDirect("pc", name) || a.useRuleWAN("pc", name) != b.useRuleWAN("pc", name) {
			t.Fatalf("rule priority/action/outage changed for %q", name)
		}
	}
	if a.useRuleWAN("pc", "private.root.test") || !a.useRuleWAN("pc", "www.root.test") {
		t.Fatal("strict first-match exception or scoped WAN fallback lost")
	}
	// Rebuilding a policy while old DNS readers still use it must not mutate
	// the previous index or introduce races between published generations.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			if compact.matches("www.root.test") {
				t.Error("published proxy rule changed")
				return
			}
		}
	}()
	for i := 0; i < 3; i++ {
		if _, err := compileMatcher(p); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestSmallCustomDomainsKeepMaps(t *testing.T) {
	m, err := compileMatcher(Policy{DirectDomains: []string{"small.test"}})
	if err != nil || m.compact != nil || !m.matches("www.small.test") {
		t.Fatal("small list path changed", err)
	}
}

func TestCompactDomainHashCollisionFallsBack(t *testing.T) {
	// The two Thue-Morse strings collide for all odd MPH multipliers. Even
	// unusual internal input must retain the map path rather than reject a
	// policy solely because the storage optimization cannot index it.
	a, b := make([]byte, 2048), make([]byte, 2048)
	for i := range a {
		parity := 0
		for n := i; n != 0; n &= n - 1 {
			parity ^= 1
		}
		a[i], b[i] = "ab"[parity], "ba"[parity]
	}
	p := Policy{DirectServiceExpressions: []string{"full:" + string(a), "full:" + string(b)}}
	for i := 0; i < compactDomainThreshold; i++ {
		p.DirectServiceExpressions = append(p.DirectServiceExpressions, fmt.Sprintf("domain:filler%d.test", i))
	}
	m, err := compileMatcher(p)
	if err != nil || m.compact != nil || !m.matches(string(a)) || !m.matches(string(b)) || !m.matches("www.filler42.test") {
		t.Fatal("hash collision rejected or changed a valid map matcher", err)
	}
}

func TestCompactDomainsReloadTransaction(t *testing.T) {
	policy := func(prefix string) Policy {
		p := Policy{ID: "main"}
		for i := 0; i < 4096; i++ {
			p.DirectDomains = append(p.DirectDomains, fmt.Sprintf("%s%d.test", prefix, i))
		}
		return p
	}
	s := &server{cache: make(map[string]*cacheEntry)}
	if err := s.updatePolicies([]Policy{policy("old")}); err != nil {
		t.Fatal(err)
	}
	old := s.matchers["main"]
	s.cache["old-answer"] = &cacheEntry{}
	bad := policy("invalid")
	bad.DirectServiceExpressions = []string{"regexp:["}
	if err := s.updatePolicies([]Policy{bad}); err == nil || !s.matchesDirect("main", "www.old42.test") || len(s.cache) != 1 {
		t.Fatal("rejected reload changed published rules or cache", err)
	}
	if err := s.updatePolicies([]Policy{policy("new")}); err != nil {
		t.Fatal(err)
	}
	if s.matchesDirect("main", "www.old42.test") || !s.matchesDirect("main", "www.new42.test") || len(s.cache) != 0 {
		t.Fatal("successful reload retained old rules/cache")
	}
	if !old.matches("www.old42.test") || old.matches("www.new42.test") {
		t.Fatal("reload mutated an index held by an existing reader")
	}
}
