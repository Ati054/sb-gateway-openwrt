package generation

import (
	"github.com/Ati054/xroute-openwrt/internal/catalog"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPrivateRecordPreservesNilEmptyAndNonemptySettings(t *testing.T) {
	for _, kind := range []string{"nil", "empty", "nonempty"} {
		t.Run(kind, func(t *testing.T) {
			n := catalog.Node{ID: "node", ProviderID: "trial", Protocol: "vless", Server: "127.0.0.1", Port: 443, Credentials: map[string]string{"uuid": "synthetic"}}
			if kind == "empty" {
				n.Settings = map[string]any{}
			}
			if kind == "nonempty" {
				n.Settings = map[string]any{"security": "tls"}
			}
			b, e := NewNodeBuilder(filepath.Join(t.TempDir(), "records"), nil)
			if e != nil {
				t.Fatal(e)
			}
			if e = b.Append(t.Context(), n.ID, n); e != nil {
				t.Fatal(e)
			}
			root, e := b.Seal(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer catalog.ReleaseRecordReaders(root.Path)
			metadata, e := ReadNodeMetadata(t.Context(), root)
			if e != nil || len(metadata) != 1 {
				t.Fatalf("metadata: %v", e)
			}
			full, e := metadata[0].Materialize(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(full.Settings, n.Settings) || catalog.Describe(full).Runtime != catalog.Describe(n).Runtime {
				t.Fatal("private round trip changed runtime identity")
			}
		})
	}
}
