package agentport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Compare worker inventory ownership separately from TLS, history and Xray.
func BenchmarkHealthPoolWorkers(b *testing.B) {
	for _, count := range []int{832, 5000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			pool := healthPool{Version: 4, ProbeLanes: 10, Outbounds: make(map[string]json.RawMessage), HealthPolicies: make(map[string]healthPolicyContract)}
			contract := healthPolicyContract{Mode: "best", Nodes: make(map[string]healthNode)}
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("node-%04d", i)
				pool.Outbounds[id] = json.RawMessage(fmt.Sprintf(`{"protocol":"vless","settings":{"vnext":[{"address":"acceptance.invalid","port":443,"users":[{"id":"00000000-0000-0000-0000-%012d"}]}]},"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"acceptance.invalid"}}}`, i))
				contract.Candidates = append(contract.Candidates, id)
				contract.Nodes[id] = healthNode{Label: "Acceptance " + id, Country: "CA", Protocol: "vless", Transport: "tcp"}
			}
			pool.HealthPolicies["resproxy-all"] = contract
			body, err := json.Marshal(pool)
			if err != nil {
				b.Fatal(err)
			}
			path := filepath.Join(b.TempDir(), "pool.json")
			if err := os.WriteFile(path, body, 0600); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				workers := make([]*xraySelectorRuntime, 11)
				cache := &healthPoolCache{}
				for lane := range workers {
					workers[lane] = newXraySelectorRuntime(Options{HealthPoolFile: path})
					workers[lane].poolCache = cache
					if _, _, err := workers[lane].Reload(); err != nil {
						b.Fatal(err)
					}
				}
				runtime.KeepAlive(workers)
			}
		})
	}
}
