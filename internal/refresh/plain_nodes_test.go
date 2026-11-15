package refresh

import (
	"github.com/Ati054/xroute-openwrt/internal/catalog"
	"github.com/Ati054/xroute-openwrt/internal/generation"
	"testing"
)

func TestRefreshPlainVLESSPreservesRecordIdentity(t *testing.T) {
	service := Service{Store: generation.Store{Root: t.TempDir(), MetadataRoot: t.TempDir()}}
	defer catalog.ReleaseRecordReaders(service.Store.IndexedRoot())
	feed := []byte("vless://11111111-1111-1111-1111-111111111111@127.0.0.1:24444?encryption=none&security=none&type=tcp#Local-A\n")
	for n := 0; n < 3; n++ {
		if _, e := service.Import(t.Context(), "trial", feed, 0); e != nil {
			t.Fatal(e)
		}
		_, nodes, e := service.Store.CurrentMetadata()
		if e != nil || len(nodes) != 1 {
			t.Fatalf("metadata: %v", e)
		}
		if _, e = nodes[0].Materialize(t.Context()); e != nil {
			t.Fatal(e)
		}
	}
}
