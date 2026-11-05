package refresh

import (
	"context"
	"reflect"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
)

func TestProviderRefreshRetainsCatalogPriority(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, metadata := range []bool{false, true} {
			name := "import"
			if live {
				name = "live"
			}
			if metadata {
				name += "-metadata"
			}
			t.Run(name, func(t *testing.T) {
				store := generation.Store{Root: t.TempDir()}
				if metadata {
					store.MetadataRoot = t.TempDir()
				}
				service := Service{Store: store}
				runtime := &fakeRuntime{previousTag: "block"}
				var applied []string
				if live {
					service.Runtime = runtime
					service.AfterSwitch = func(_, next []catalog.Node) (func() error, error) {
						applied = nodeOrder(next)
						return nil, nil
					}
				}
				refresh := func(provider, feed string) {
					t.Helper()
					var err error
					if live {
						_, err = service.Apply(context.Background(), provider, []byte(feed), 10, "main", firstNode)
					} else {
						_, err = service.Import(context.Background(), provider, []byte(feed), 10)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				refresh("first", linkCA)
				refresh("second", linkUS)
				_, initial, err := store.Current()
				if err != nil {
					t.Fatal(err)
				}
				want := nodeOrder(initial)
				for _, provider := range []string{"first", "second", "first", "second"} {
					feed := linkCA
					if provider == "second" {
						feed = linkUS
					}
					refresh(provider, feed)
					_, current, err := store.Current()
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(nodeOrder(current), want) {
						t.Fatalf("unchanged provider %s reordered persisted priority: %v; want %v", provider, nodeOrder(current), want)
					}
					if live && (!reflect.DeepEqual(applied, want) || runtime.lastSelected != want[0]) {
						t.Fatalf("unchanged provider %s changed live priority: pool=%v selected=%s; want %v", provider, applied, runtime.lastSelected, want)
					}
				}
			})
		}
	}
}

func nodeOrder(nodes []catalog.Node) []string {
	result := make([]string, len(nodes))
	for i, node := range nodes {
		result[i] = node.ID
	}
	return result
}

func TestProviderGrowthKeepsSourcePositionAndIncomingOrder(t *testing.T) {
	service := Service{Store: generation.Store{Root: t.TempDir()}}
	for _, item := range []struct{ provider, feed string }{
		{"first", linkCA}, {"second", linkUS}, {"first", linkUS + "\n" + linkCA},
	} {
		if _, err := service.Import(context.Background(), item.provider, []byte(item.feed), 10); err != nil {
			t.Fatal(err)
		}
	}
	_, current, err := service.Store.Current()
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 3 || current[0].ProviderID != "first" || current[0].Country != "US" ||
		current[1].ProviderID != "first" || current[1].Country != "CA" || current[2].ProviderID != "second" {
		t.Fatalf("source growth displaced another source or lost incoming order: %+v", current)
	}
}
