package refresh

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
	"github.com/Ati054/sb-gateway-openwrt/internal/subscriptioncatalog"
)

func TestRefreshThroughRuntimeDirectoryAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix runtime directory aliases")
	}
	for _, layout := range []string{"var-to-tmp", "var-run-to-run"} {
		t.Run(layout, func(t *testing.T) {
			base := t.TempDir()
			var runRoot string
			if layout == "var-to-tmp" {
				if err := os.MkdirAll(filepath.Join(base, "tmp", "run"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("tmp", filepath.Join(base, "var")); err != nil {
					t.Fatal(err)
				}
				runRoot = filepath.Join(base, "var", "run", "resproxy")
			} else {
				for _, name := range []string{"var", "run"} {
					if err := os.Mkdir(filepath.Join(base, name), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink("../run", filepath.Join(base, "var", "run")); err != nil {
					t.Fatal(err)
				}
				runRoot = filepath.Join(base, "var", "run", "resproxy")
			}
			store := generation.Store{Root: filepath.Join(base, "data"), MetadataRoot: runRoot}
			defer catalog.ReleaseRecordReaders(store.IndexedRoot())
			service := Service{Store: store}
			for _, source := range []struct{ id, body string }{
				{"first", linkCA},
				{"second", linkUS},
				{"first", linkCA + "\n" + strings.Replace(linkCA, "ca.example.test", "new.example.test", 1)},
			} {
				if _, err := service.Import(t.Context(), source.id, []byte(source.body), 0); err != nil {
					t.Fatalf("import through runtime alias: %v", err)
				}
			}
			_, nodes, err := store.CurrentMetadata()
			if err != nil || len(nodes) != 3 {
				t.Fatalf("two-source catalog replacement: count=%d err=%v", len(nodes), err)
			}
			counts := map[string]int{}
			for _, node := range nodes {
				if _, err := node.Materialize(t.Context()); err != nil {
					t.Fatal(err)
				}
				counts[node.ProviderID]++
			}
			if counts["first"] != 2 || counts["second"] != 1 {
				t.Fatal("refresh replaced an unrelated source")
			}
			left, err := filepath.Glob(filepath.Join(runRoot, ".subscription-import-*"))
			if err != nil || len(left) != 0 {
				t.Fatal("private import staging was not removed")
			}
		})
	}
}

func TestRuntimeAliasSupportDoesNotAcceptSymlinkedPublishedCatalog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink protection")
	}
	store := generation.Store{Root: t.TempDir(), MetadataRoot: t.TempDir()}
	outside := t.TempDir()
	if err := os.Symlink(outside, store.IndexedRoot()); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: store}
	if _, err := service.Import(t.Context(), "first", []byte(linkCA), 0); !errors.Is(err, subscriptioncatalog.ErrUnsafePath) {
		t.Fatalf("symlinked published root accepted: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("import wrote outside its private published root")
	}
}
