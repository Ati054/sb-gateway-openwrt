package catalog

import (
	"testing"
)

func TestInformationalLabelsDoNotHideRealChanges(t *testing.T) {
	before := node("Germany | Remaining 50 GB", "DE", "node.example", "key")
	before.ID = "node"
	before.SelectionIdentity = "stable"
	for _, name := range []string{"Germany | Remaining 49 GB", "Germany | Remaining 1.5 TB"} {
		after := before
		after.Label = name
		if !InformationalChangesOnly([]Node{before}, []Node{after}) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"France | Remaining 49 GB", "Germany 2", "Germany | Remaining 50 GB new name"} {
		after := before
		after.Label = name
		if InformationalChangesOnly([]Node{before}, []Node{after}) {
			t.Fatal("real rename discarded", name)
		}
	}
	after := before
	after.Label = "Germany | Remaining 49 GB"
	after.Server = "other.example"
	if InformationalChangesOnly([]Node{before}, []Node{after}) {
		t.Fatal("endpoint change discarded")
	}
	for _, pair := range [][2]string{{"RU Осталось: 50 ГБ", "RU Осталось: 49 ГБ"}, {"US Expiry: 2026-11-20", "US Expiry: 2026-12-20"}, {"剩余流量：50GB", "剩余流量：49GB"}} {
		a, b := before, before
		a.Label = pair[0]
		b.Label = pair[1]
		if !InformationalChangesOnly([]Node{a}, []Node{b}) {
			t.Fatal(pair)
		}
	}
}
