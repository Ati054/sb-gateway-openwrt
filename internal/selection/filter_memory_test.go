package selection

import (
	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"fmt"
	"testing"
)

func BenchmarkFilterCatalog(b *testing.B) {
	for _, count := range []int{832, 5000} {
		for _, filter := range []struct {
			name  string
			value Filter
		}{
			{"all", Filter{}},
			{"name", Filter{Include: "⚡️, premium", Exclude: "test, тест"}},
		} {
			b.Run(fmt.Sprintf("%d/%s", count, filter.name), func(b *testing.B) {
				nodes := make([]catalog.Node, count)
				for i := range nodes {
					nodes[i] = catalog.Node{Label: fmt.Sprintf("⚡️ Premium Canada %04d", i), Country: "CA", Protocol: "vless"}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for _, node := range nodes {
						if !filter.value.Matches(node) {
							b.Fatal("matching node excluded")
						}
					}
				}
			})
		}
	}
}
