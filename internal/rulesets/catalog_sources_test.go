package rulesets

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReviewedCountrySources(t *testing.T) {
	root := os.Getenv("RESPROXY_CATALOG_SOURCE_CACHE")
	if root == "" {
		t.Skip("set RESPROXY_CATALOG_SOURCE_CACHE to the reviewed source cache")
	}
	known, err := index()
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]Rules{}
	for _, id := range featuredIDs {
		item := known[id]
		if item.UpstreamName == "" || item.Country != "cn" && item.Country != "ir" {
			continue
		}
		compiled, err := Compile(context.Background(), item.UpstreamName, func(_ context.Context, name string) (string, error) {
			b, e := os.ReadFile(filepath.Join(root, name))
			return string(b), e
		})
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		results[id] = compiled
		t.Logf("%s: %d compiled matches", id, compiled.Count())
	}
	if output := os.Getenv("RESPROXY_CATALOG_AUDIT_OUTPUT"); output != "" {
		body, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(output, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
