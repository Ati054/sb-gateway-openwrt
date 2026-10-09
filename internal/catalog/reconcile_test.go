package catalog

import (
	"fmt"
	"reflect"
	"testing"
)

func node(label, country, server, credential string) Node {
	return Node{
		ProviderID: "provider-a", Label: label, Country: country,
		Protocol: "vless", Transport: "ws", Server: server, Port: 443,
		Settings:    map[string]any{"tls": "on", "path": "/proxy"},
		Credentials: map[string]string{"uuid": credential},
	}
}

func TestReconcilePreservesUnambiguousProviderEdits(t *testing.T) {
	old, _, err := Reconcile("provider-a", nil, []Node{node("CA Toronto", "CA", "old.example", "uuid-1")})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*Node)
	}{
		{"cosmetic rename", func(n *Node) { n.Label = "CA Toronto ⭐" }},
		{"credential rotation", func(n *Node) { n.Credentials["uuid"] = "uuid-2" }},
		{"endpoint change", func(n *Node) { n.Server = "new.example" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fresh := node("CA Toronto", "CA", "old.example", "uuid-1")
			test.edit(&fresh)
			got, delta, err := Reconcile("provider-a", old, []Node{fresh})
			if err != nil {
				t.Fatal(err)
			}
			if got[0].ID != old[0].ID || got[0].SelectionIdentity != old[0].SelectionIdentity {
				t.Fatalf("selection lost: old=%+v new=%+v", old[0], got[0])
			}
			if len(delta.Added) != 0 || len(delta.Removed) != 0 {
				t.Fatalf("replacement invented: %+v", delta)
			}
		})
	}
}

func TestReconcileDoesNotGuessDifferentConnection(t *testing.T) {
	old, _, _ := Reconcile("provider-a", nil, []Node{node("CA Toronto", "CA", "old.example", "uuid-1")})
	cases := []struct {
		name string
		node Node
	}{
		{"label and endpoint changed", node("CA Calgary", "CA", "new.example", "uuid-1")},
		{"country changed", node("US Toronto", "US", "old.example", "uuid-1")},
		{"country metadata changed", node("CA Toronto", "US", "old.example", "uuid-1")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, delta, err := Reconcile("provider-a", old, []Node{test.node})
			if err != nil {
				t.Fatal(err)
			}
			if got[0].ID == old[0].ID || got[0].SelectionIdentity == old[0].SelectionIdentity || len(delta.Added) != 1 || len(delta.Removed) != 1 {
				t.Fatalf("unsafe identity inheritance: got=%+v delta=%+v", got[0], delta)
			}
		})
	}
}

func TestReconcileRefusesAmbiguousEndpointRename(t *testing.T) {
	first := node("First", "CA", "shared.example", "uuid-1")
	second := node("Second", "CA", "shared.example", "uuid-2")
	old, _, _ := Reconcile("provider-a", nil, []Node{first, second})
	fresh := node("Renamed", "CA", "shared.example", "uuid-1")
	got, delta, err := Reconcile("provider-a", old, []Node{fresh})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID == old[0].ID || got[0].ID == old[1].ID || len(delta.Added) != 1 {
		t.Fatalf("ambiguous endpoint reused: %+v %+v", got, delta)
	}
}

func TestReconcileDuplicateAccountsRemainDistinctAfterReorder(t *testing.T) {
	first := node("Same", "CA", "shared.example", "uuid-1")
	second := node("Same", "CA", "shared.example", "uuid-2")
	old, _, _ := Reconcile("provider-a", nil, []Node{first, second})
	got, delta, err := Reconcile("provider-a", old, []Node{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != old[1].ID || got[1].ID != old[0].ID || len(delta.Added) != 0 || len(delta.Removed) != 0 {
		t.Fatalf("duplicate accounts drifted: old=%+v new=%+v delta=%+v", old, got, delta)
	}
}

func TestReconcileLargeReorderedFeedAndInvalidReplacement(t *testing.T) {
	feed := make([]Node, 1000)
	for i := range feed {
		feed[i] = node(fmt.Sprintf("CA Node %d", i), "CA", fmt.Sprintf("edge-%d.example", i), fmt.Sprintf("uuid-%d", i))
	}
	old, _, err := Reconcile("provider-a", nil, feed)
	if err != nil {
		t.Fatal(err)
	}
	reversed := make([]Node, len(feed))
	for i := range feed {
		reversed[i] = feed[len(feed)-1-i]
	}
	got, delta, err := Reconcile("provider-a", old, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(SortedIDs(got), SortedIDs(old)) || len(delta.Added)+len(delta.Removed)+len(delta.RuntimeChanged) != 0 {
		t.Fatalf("reordered feed changed catalog: %+v", delta)
	}
	if _, _, err := Reconcile("provider-a", old, nil); err == nil {
		t.Fatal("empty feed replaced working generation")
	}
	foreign := node("Foreign", "CA", "foreign.example", "uuid")
	foreign.ProviderID = "provider-b"
	if _, _, err := Reconcile("provider-a", old, []Node{foreign}); err == nil {
		t.Fatal("foreign node accepted")
	}
}

func BenchmarkReconcileStable1000(b *testing.B) {
	feed := make([]Node, 1000)
	for i := range feed {
		feed[i] = node(fmt.Sprintf("CA Node %d", i), "CA", fmt.Sprintf("edge-%d.example", i), fmt.Sprintf("uuid-%d", i))
	}
	old, _, err := Reconcile("provider-a", nil, feed)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := Reconcile("provider-a", old, feed); err != nil {
			b.Fatal(err)
		}
	}
}
