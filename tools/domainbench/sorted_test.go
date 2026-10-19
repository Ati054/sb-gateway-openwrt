package main

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

func TestIndexMatchesMapSemantics(t *testing.T) {
	var b Builder
	exact := map[string]bool{"exact.test": true, "both.test": true, "nested.root.test": true}
	suffixes := map[string]bool{"root.test": true, "both.test": true, "xn--p1ai": true, ".dot.test": true}
	for name := range exact {
		b.AddExact(strings.ToUpper(name))
		b.AddExact(name)
	}
	for name := range suffixes {
		b.AddSuffix(name)
		b.AddSuffix(strings.ToUpper(name))
	}
	m := b.Build()
	if len(b.data) != 0 || len(b.entries) != 0 {
		t.Fatal("builder retains input after Build")
	}
	names := []string{"", "exact.test", "www.exact.test", "root.test", "a.root.test", "notroot.test", "both.test", "a.both.test", "nested.root.test", "a.nested.root.test", "xn--p1ai", "www.xn--p1ai", "root.test.", "dot.test", "a.dot.test", ".dot.test", "a..dot.test"}
	for _, name := range names {
		want := exact[name]
		for suffix := name; suffix != ""; {
			want = want || suffixes[suffix]
			_, next, ok := strings.Cut(suffix, ".")
			if !ok {
				break
			}
			suffix = next
		}
		if got := m.Match(name); got != want {
			t.Errorf("Match(%q) = %v, want %v", name, got, want)
		}
	}
	if n := testing.AllocsPerRun(100, func() { m.Match("a.root.test") }); n != 0 {
		t.Fatalf("query allocations = %v", n)
	}
	// Reusing a consumed builder must not mutate an already published index.
	b.AddSuffix("other.test")
	next := b.Build()
	if m.Match("other.test") || !m.Match("root.test") || next.Match("root.test") {
		t.Fatal("indexes share mutable build storage")
	}
}

func TestIndexDifferential(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var b Builder
	exact, suffixes := map[string]bool{}, map[string]bool{}
	for i := 0; i < 10000; i++ {
		name := fmt.Sprintf("d%d.p%d.test", r.IntN(1000), r.IntN(13))
		if r.IntN(2) == 0 {
			b.AddExact(name)
			exact[name] = true
		} else {
			b.AddSuffix(name)
			suffixes[name] = true
		}
	}
	m := b.Build()
	for i := 0; i < 30000; i++ {
		name := fmt.Sprintf("d%d.p%d.test", r.IntN(1500), r.IntN(13))
		if r.IntN(2) == 0 {
			name = "www." + name
		}
		want := exact[name]
		for suffix := name; suffix != ""; {
			want = want || suffixes[suffix]
			_, next, found := strings.Cut(suffix, ".")
			if !found {
				break
			}
			suffix = next
		}
		if got := m.Match(name); got != want {
			t.Fatalf("Match(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestEmptyIndex(t *testing.T) {
	var b Builder
	m := b.Build()
	if m.Bytes() != 0 || m.Match("") || m.Match("example.test") {
		t.Fatal("empty index matches")
	}
}
