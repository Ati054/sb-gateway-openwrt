// Adapted from SB Gateway 1.6.45 (AGPL-3.0).
package agentport

import (
	"strings"
	"testing"
	"time"
)

func TestPriorityMembershipEditsRetainActiveUntilFreshRecovery(t *testing.T) {
	for _, candidates := range [][]string{
		{"active", "reserve", "added"}, {"added", "active", "reserve"}, {"active", "added", "reserve"},
	} {
		t.Run(strings.Join(candidates, "-"), func(t *testing.T) {
			controller, item, runtime := stagedOptimizationController(t)
			contract := runtime.pool.HealthPolicies["europe"]
			contract.Mode, contract.Candidates = "priority", candidates
			contract.Policy.ProbeBatchSize = 3
			contract.Nodes["added"] = healthNode{Label: "Added"}
			runtime.pool.HealthPolicies["europe"] = contract
			item.Mode, item.Selected, item.RuntimeSelected = "priority", "reserve", "reserve"
			runtime.current["europe"] = "reserve"
			runtime.probes["active"], runtime.probes["added"] = failedEvidence(), successfulEvidence(100)
			if err := controller.Tick(time.Unix(1060, 0)); err != nil {
				t.Fatal(err)
			}
			if item.Selected != "reserve" || hasSelection(runtime.selections, "europe", "active") {
				t.Fatalf("membership-only edit acted as a reorder: selected=%q selections=%v",
					item.Selected, runtime.selections)
			}
		})
	}
}
