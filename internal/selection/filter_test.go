package selection

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

func TestFilterPreservesSubscriptionOrderAndUpdatesMembership(t *testing.T) {
	nodes := []catalog.Node{
		{ID: "one", ProviderID: "Source A", Country: "CA", Protocol: "vless", Label: "🇨🇦 Premium Fastest"},
		{ID: "test", ProviderID: "Source A", Country: "CA", Protocol: "vless", Label: "🇨🇦 premium TEST"},
		{ID: "other", ProviderID: "Source B", Country: "CA", Protocol: "vless", Label: "🇨🇦 premium"},
		{ID: "wrongprotocol", ProviderID: "Source A", Country: "CA", Protocol: "trojan", Label: "🇨🇦 premium"},
		{ID: "wrongcountry", ProviderID: "Source A", Country: "US", Protocol: "vless", Label: "🇨🇦 premium"},
	}
	filter := Filter{Subscription: "Source A", Countries: []string{"CA"}, Protocols: []string{"vless"}, Include: "🇨🇦, PREMIUM", Exclude: "test"}
	selected, _, err := Candidates(nodes, []string{"all:*"}, filter)
	if err != nil || !reflect.DeepEqual(selected, []string{"one"}) {
		t.Fatalf("filtered=%v err=%v", selected, err)
	}
	nodes = append(nodes, catalog.Node{ID: "new", ProviderID: "Source A", Country: "CA", Protocol: "vless", Label: "🇨🇦 premium new"})
	selected, _, err = Candidates(nodes, []string{"all:*"}, filter)
	if err != nil || !reflect.DeepEqual(selected, []string{"one", "new"}) {
		t.Fatalf("updated filter=%v err=%v", selected, err)
	}
	selected, _, err = Candidates(nodes, []string{"subscription:Source%20A"}, filter)
	if err != nil || !reflect.DeepEqual(selected, []string{"one", "new"}) {
		t.Fatalf("source selection=%v err=%v", selected, err)
	}
	for _, invalid := range []Filter{{Countries: []string{"ca"}}, {Protocols: []string{"unknown"}}, {Exclude: "two words"}, {Include: "bad\ninput"}, {Include: strings.Repeat("x", 513)}} {
		if _, _, err = Candidates(nodes, []string{"all:*"}, invalid); err == nil {
			t.Fatalf("invalid filter accepted: %+v", invalid)
		}
	}
}

func TestFilterEmojiVariationAndWholeWords(t *testing.T) {
	filter := Filter{Include: "⚡️, premium", Exclude: "test, тест"}
	for label, want := range map[string]bool{"⚡ premium fastest": true, "⚡️ Premium тестовый": true, "⚡ premium (TEST)": false, "⚡ premium [ТЕСТ]": false, "⚡ standard": false} {
		if got := filter.Matches(catalog.Node{Label: label}); got != want {
			t.Fatalf("label %q: %v want %v", label, got, want)
		}
	}
}

func TestPublicInventoryHasNoSecretsAndPreservesFeedOrder(t *testing.T) {
	nodes := []catalog.Node{
		{ID: "z", SelectionIdentity: "0123456789abcdef01234567", Country: "CA", City: "Toronto", ProviderID: "source", Label: "last alphabetically", Server: "private.example", Settings: map[string]any{"secret": "hidden"}, Credentials: map[string]string{"password": "secret"}},
		{ID: "a", SelectionIdentity: "abcdef0123456789abcdef01", Country: "CA", City: "Montreal", ProviderID: "source", Label: "first alphabetically"},
		{ID: "unknown", SelectionIdentity: "fedcba0123456789abcdef01", ProviderID: "source", Label: "unknown geography"},
	}
	inventory := PublicInventory(nodes)
	if inventory.Nodes[0].ID != "z" || inventory.Nodes[1].ID != "a" || len(inventory.Nodes) != 3 {
		t.Fatal("inventory reordered or lost unknown geography")
	}
	body, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "private.example") || strings.Contains(string(body), "password") || strings.Contains(string(body), "secret") {
		t.Fatal("public inventory leaked private connection data")
	}
	if len(inventory.Locations) != 3 {
		t.Fatalf("country/cities missing: %+v", inventory.Locations)
	}
}
