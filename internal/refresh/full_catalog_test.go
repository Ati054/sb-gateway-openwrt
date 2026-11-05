package refresh

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
)

func TestImportWholeSubscriptionBeyondOldNodeLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("full catalogue integration")
	}
	const count = 6001
	var feed strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&feed, "vless://2f1c08fc-3f43-4e95-8f57-f8bded750a1a@node-%d.example:443?security=tls#Node-%d\n", i, i)
	}
	s := &Service{Store: generation.Store{Root: t.TempDir(), MetadataRoot: t.TempDir()}}
	defer catalog.ReleaseRecordReaders(s.Store.IndexedRoot())
	result, err := s.Import(t.Context(), "large-source", []byte(feed.String()), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, nodes, err := s.Store.CurrentMetadata()
	if err != nil || len(nodes) != count || result.ParseStats.Limited != 0 {
		t.Fatalf("catalogue truncated: nodes=%d stats=%+v err=%v", len(nodes), result.ParseStats, err)
	}
	for _, index := range []int{0, 999, 1000, 4999, 5000, count - 1} {
		node := nodes[index]
		if node.Settings != nil || node.Credentials != nil || node.Record == nil {
			t.Fatal("private connection retained in the metadata view")
		}
		full, err := node.Materialize(t.Context())
		if err != nil || full.Server != fmt.Sprintf("node-%d.example", index) || full.Credentials["uuid"] == "" {
			t.Fatalf("node %d not available: %v", index, err)
		}
	}
}
