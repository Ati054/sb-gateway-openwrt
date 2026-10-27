package dnsproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
)

func TestPolicyMatchersShareOnlyIdenticalRouting(t *testing.T) {
	policies := sharingPolicies(2, 1, 512, "")
	policies[0].Rules[1].Domains = append(policies[0].Rules[1].Domains, "domain:test")
	policies[1].Rules = policies[0].Rules
	matchers, err := compilePolicyMatchers(policies)
	if err != nil {
		t.Fatal(err)
	}
	left, right := matchers[policies[0].ID], matchers[policies[1].ID]
	if &left.ordered[0] != &right.ordered[0] || left.ordered[1].matcher.compact != right.ordered[1].matcher.compact {
		t.Fatal("identical routing was compiled twice for different client assignments")
	}
	for _, tc := range []struct {
		name   string
		change func(*Policy)
	}{
		{"default-outage", func(p *Policy) { p.ProxyOutageAction = "wan" }},
		{"rule-order", func(p *Policy) { p.Rules[0], p.Rules[1] = p.Rules[1], p.Rules[0] }},
		{"rule-action", func(p *Policy) { p.Rules[0].Action = "direct" }},
		{"rule-outage", func(p *Policy) { p.Rules[0].OutageAction = "wan" }},
		{"rule-domain", func(p *Policy) { p.Rules[0].Domains = []string{"full:other.test"} }},
		{"direct-domain", func(p *Policy) { p.DirectDomains = []string{"example.invalid"} }},
		{"expression", func(p *Policy) { p.DirectServiceExpressions = []string{"keyword:needle"} }},
		{"zone", func(p *Policy) { p.RUZoneMode = "both" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(policies[1])
			var changed Policy
			if err := json.Unmarshal(body, &changed); err != nil {
				t.Fatal(err)
			}
			tc.change(&changed)
			built, err := compilePolicyMatchers([]Policy{policies[0], changed})
			if err != nil {
				t.Fatal(err)
			}
			a, b := built[policies[0].ID], built[changed.ID]
			if &a.ordered[0] == &b.ordered[0] {
				t.Fatal("different routing semantics were merged")
			}
			legacy, err := compileMatcher(changed)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"private.test", "www.d42.set0.test", "example.invalid", "site.ru", "a-needle.invalid"} {
				if b.matches(name) != legacy.matches(name) {
					t.Fatalf("matching changed for %s", name)
				}
			}
			if tc.name == "rule-order" && (a.matches("private.test") || !b.matches("private.test")) {
				t.Fatal("first matching rule lost priority")
			}
		})
	}
}

func TestSharedPolicyMatchersReloadWithoutMutatingReaders(t *testing.T) {
	policies := sharingPolicies(2, 1, 512, "")
	s := &server{cache: make(map[string]*cacheEntry)}
	if err := s.updatePolicies(policies); err != nil {
		t.Fatal(err)
	}
	old := s.matchers[policies[0].ID]
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for i := 0; i < 10000; i++ {
			if !old.matches("www.d42.set0.test") {
				t.Error("old immutable matcher changed")
				return
			}
		}
	}()
	for i := 0; i < 3; i++ {
		if err := s.updatePolicies(policies); err != nil {
			t.Fatal(err)
		}
	}
	readers.Wait()
	latest := s.matchers[policies[0].ID]
	if old.ordered[1].matcher.compact == latest.ordered[1].matcher.compact {
		t.Fatal("new generation reused an old custom index")
	}
	policies[1].Rules = []Rule{{Action: "direct", Domains: []string{"unknown:bad"}}}
	if err := s.updatePolicies(policies); err == nil {
		t.Fatal("invalid reload accepted")
	}
	if !s.matchesDirect(policies[1].ID, "www.d42.set0.test") {
		t.Fatal("invalid reload replaced the published matchers")
	}
}

func TestSharedPolicyMatchersKeepProxyRoutesSeparate(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		value := byte(calls.Add(1))
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(cacheableDoHReply(query, value))
	}))
	defer upstream.Close()
	s := wanSharingServer(t, upstream.Client(), upstream.URL+"/dns-query")
	policies := []Policy{{ID: "main", DirectDomains: []string{"direct.test"}}, {ID: "other", DirectDomains: []string{"direct.test"}}}
	var err error
	s.matchers, err = compilePolicyMatchers(policies)
	if err != nil {
		t.Fatal(err)
	}
	s.proxySelections = map[string]string{"resproxy-main": "leaf-a", "resproxy-other": "leaf-a"}
	for i, source := range []string{"198.51.100.8", "192.0.2.8"} {
		answer := s.resolve(context.Background(), dnsQuery("example.com"), netip.MustParseAddr(source))
		if answer[len(answer)-1] != byte(i+1) {
			t.Fatal("different proxy policies shared a response")
		}
	}
	for _, source := range []string{"198.51.100.8", "192.0.2.8"} {
		answer := s.resolve(context.Background(), dnsQuery("direct.test"), netip.MustParseAddr(source))
		if answer[len(answer)-1] != 3 {
			t.Fatal("identical WAN route stopped sharing its response")
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected number of upstream requests: %d", calls.Load())
	}
	s.proxySelections["resproxy-main"] = "leaf-b"
	answer := s.resolve(context.Background(), dnsQuery("example.com"), netip.MustParseAddr("198.51.100.8"))
	if answer[len(answer)-1] != 4 {
		t.Fatal("changed exit reused an earlier proxy response")
	}
	answer = s.resolve(context.Background(), dnsQuery("example.com"), netip.MustParseAddr("192.0.2.8"))
	if answer[len(answer)-1] != 2 || calls.Load() != 4 {
		t.Fatal("one policy's exit change disturbed another policy")
	}
}

// Keep the previous implementation as the benchmark baseline.
func reviewMatcherSet(policies []Policy, share bool) (map[string]domainMatcher, error) {
	if share {
		return compilePolicyMatchers(policies)
	}
	result := make(map[string]domainMatcher, len(policies))
	for _, policy := range policies {
		matcher, err := compileMatcher(policy)
		if err != nil {
			return nil, err
		}
		result[policy.ID] = matcher
	}
	return result, nil
}

func sharingPolicies(count, unique, domains int, asset string) []Policy {
	policies := make([]Policy, count)
	for i := range policies {
		policy := Policy{ID: fmt.Sprintf("route-%d", i), SourceCIDRs: []string{fmt.Sprintf("192.0.2.%d/32", i+1)}, ProxyOutageAction: "block"}
		list := make([]string, 0, domains)
		if asset != "" {
			list = append(list, asset)
		} else {
			for j := 0; j < domains; j++ {
				list = append(list, fmt.Sprintf("domain:d%d.set%d.test", j, i%unique))
			}
		}
		policy.Rules = []Rule{{Action: "proxy", OutageAction: "block", Domains: []string{"full:private.test"}}, {Action: "direct", Domains: list}}
		policies[i] = policy
	}
	return policies
}

func TestDNSPolicySharingRetainedMemoryReview(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XRAY_LOCATION_ASSET", root)
	names := sharingPolicies(1, 1, 4096, "")[0].Rules[1].Domains
	asset, err := geoipasset.PublishDomains(root, "shared-review", names)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name     string
		policies []Policy
	}{
		{"8-identical-custom-4096", sharingPolicies(8, 1, 4096, "")},
		{"8-distinct-custom-4096", sharingPolicies(8, 8, 4096, "")},
		{"8-identical-shared-asset-4096", sharingPolicies(8, 1, 4096, asset)},
		{"57-groups-8-policies-custom-4096-synthetic", sharingPolicies(57, 8, 4096, "")},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			// Warm up geodata's existing shared asset cache in both variants.
			warm, err := reviewMatcherSet(fixture.policies, false)
			if err != nil {
				t.Fatal(err)
			}
			runtime.KeepAlive(warm)
			measure := func(share bool) uint64 {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				sets := make([]map[string]domainMatcher, 8)
				for i := range sets {
					sets[i], err = reviewMatcherSet(fixture.policies, share)
					if err != nil {
						t.Fatal(err)
					}
				}
				runtime.GC()
				runtime.ReadMemStats(&after)
				runtime.KeepAlive(sets)
				if after.HeapAlloc <= before.HeapAlloc {
					return 0
				}
				return (after.HeapAlloc - before.HeapAlloc) / uint64(len(sets))
			}
			legacy, shared := measure(false), measure(true)
			t.Logf("retained bytes per matcher set: separate=%d shared=%d", legacy, shared)
			runtime.KeepAlive(warm)
		})
	}
}

func BenchmarkDNSPolicySharingReview(b *testing.B) {
	for _, unique := range []int{1, 8} {
		policies := sharingPolicies(8, unique, 4096, "")
		for _, shared := range []bool{false, true} {
			b.Run(fmt.Sprintf("8-lists-%d-unique/shared=%v", unique, shared), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					set, err := reviewMatcherSet(policies, shared)
					if err != nil {
						b.Fatal(err)
					}
					runtime.KeepAlive(set)
				}
			})
		}
	}
}
