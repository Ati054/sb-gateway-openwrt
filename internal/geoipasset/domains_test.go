package geoipasset

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDomainAssetImmutableSharedAndRetained(t *testing.T) {
	root := t.TempDir()
	ref, err := PublishDomains(root, "cn", []string{"domain:example.cn", "full:exact.test", "keyword:needle", "regexp:^regex\\.test$"})
	if err != nil {
		t.Fatal(err)
	}
	same, err := PublishDomains(root, "cn", []string{"domain:example.cn", "full:exact.test", "keyword:needle", "regexp:^regex\\.test$"})
	if err != nil || same != ref {
		t.Fatal(same, ref, err)
	}
	if err := VerifyDomain(root, ref); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReferenceName(ref); ok {
		t.Fatal("GeoSite accepted as GeoIP")
	}
	name, ok := DomainReferenceName(ref)
	if !ok {
		t.Fatal(ref)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(root, name), old, old); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(root, nil, nil, []byte(`{"domain":["`+ref+`"]}`), time.Now()); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if VerifyDomain(root, ref) == nil {
		t.Fatal("damaged domain asset accepted")
	}
}
