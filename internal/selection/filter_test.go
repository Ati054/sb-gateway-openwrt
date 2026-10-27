package selection

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

func TestFilterPreservesSubscriptionOrderAndUpdatesMembership(t *testing.T) {
	nodes := []catalog.Node{
		{ID: "one", ProviderID: "Source A", Country: "CA", Protocol: "vless", Label: "🇨🇦 Premium Fast"},
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
	for _, invalid := range []Filter{{Countries: []string{"ca"}}, {Protocols: []string{"unknown"}}, {Exclude: "bad\ninput"}, {Include: "bad\ninput"}, {Include: strings.Repeat("x", 513)}, {Exclude: strings.Repeat("я", 257)}} {
		if _, _, err = Candidates(nodes, []string{"all:*"}, invalid); err == nil {
			t.Fatalf("invalid filter accepted: %+v", invalid)
		}
	}
}

func TestFilterQuotedNamesSharedWithLuCI(t *testing.T) {
	body, err := os.ReadFile("testdata/name_filters.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Pattern string `json:"pattern"`
		Label   string `json:"label"`
		Match   bool   `json:"match"`
		Invalid bool   `json:"invalid"`
	}
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		for _, excluded := range []bool{false, true} {
			f := Filter{Include: c.Pattern}
			if excluded {
				f = Filter{Exclude: c.Pattern}
			}
			if err := f.Validate(); (err != nil) != c.Invalid {
				t.Fatalf("validation %+v excluded=%v: %v", c, excluded, err)
			}
			if c.Invalid {
				if _, _, err := Candidates([]catalog.Node{{ID: "node", Label: c.Label}}, []string{"all:*"}, f); err == nil {
					t.Fatal("malformed filter admitted candidates")
				}
				continue
			}
			want := c.Match
			if excluded {
				want = !want
			}
			if got := f.Matches(catalog.Node{Label: c.Label}); got != want {
				t.Fatalf("match %+v excluded=%v: %v", c, excluded, got)
			}
			ids, _, err := Candidates([]catalog.Node{{ID: "node", Label: c.Label}}, []string{"all:*"}, f)
			if err != nil || (len(ids) == 1) != want {
				t.Fatalf("compiled catalog match %+v excluded=%v: %v %v", c, excluded, ids, err)
			}
		}
	}
}

func TestFilterEmojiVariationAndExcludedFragments(t *testing.T) {
	filter := Filter{Include: "⚡️, premium", Exclude: "test, тест"}
	for label, want := range map[string]bool{"⚡ premium fastest": false, "⚡️ Premium тестовый": false, "⚡ premium (TEST)": false, "⚡ premium [ТЕСТ]": false, "⚡ standard": true, "Premium Canada": true, "standard": false} {
		if got := filter.Matches(catalog.Node{Label: label}); got != want {
			t.Fatalf("label %q: %v want %v", label, got, want)
		}
	}
}

func TestFilterCommaAlternativesAndExclusionPrecedence(t *testing.T) {
	nodes := []catalog.Node{
		{ID: "amsterdam", ProviderID: "feed", Country: "NL", Protocol: "vless", Label: "Амстердам Premium"},
		{ID: "sweden", ProviderID: "feed", Country: "SE", Protocol: "vless", Label: "Швеция Standard"},
		{ID: "excluded", ProviderID: "feed", Country: "SE", Protocol: "vless", Label: "Швеция тестовый"},
		{ID: "other-source", ProviderID: "other", Country: "SE", Protocol: "vless", Label: "Швеция"},
		{ID: "other-protocol", ProviderID: "feed", Country: "SE", Protocol: "trojan", Label: "Швеция"},
		{ID: "other-country", ProviderID: "feed", Country: "DE", Protocol: "vless", Label: "Швеция"},
		{ID: "unmatched", ProviderID: "feed", Country: "NL", Protocol: "vless", Label: "Роттердам"},
	}
	f := Filter{Subscription: "feed", Countries: []string{"NL", "SE"}, Protocols: []string{"vless"}, Include: " Амстердам, ШВЕЦИЯ, , ", Exclude: " недоступен, ТЕСТ, "}
	got, _, err := Candidates(nodes, []string{"all:*"}, f)
	if err != nil || !reflect.DeepEqual(got, []string{"amsterdam", "sweden"}) {
		t.Fatalf("alternatives, exclusions or scope lost: %v %v", got, err)
	}
	for _, check := range []struct {
		include, exclude, label string
		want                    bool
	}{
		{" , , ", " , ", "Any node", true},
		{"Нью Йорк, Стокгольм", "", "Premium Нью Йорк 1", true},
		{"Нью Йорк, Стокгольм", "", "Нью Москва Йорк", false},
		{"", "Нью Йорк, тест", "Premium Нью Йорк 1", false},
		{"a.b", "", "a.b node", true},
		{"a.b", "", "axb node", false},
		{"", "a.b", "axb node", true},
	} {
		f := Filter{Include: check.include, Exclude: check.exclude}
		if err := f.Validate(); err != nil {
			t.Fatal(err)
		}
		if got := f.Matches(catalog.Node{Label: check.label}); got != check.want {
			t.Fatalf("%+v: got %v", check, got)
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
