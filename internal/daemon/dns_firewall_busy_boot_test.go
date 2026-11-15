package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirewallInspectionRecoversFromBusyBootWithoutTrustingAbortedSnapshot(t *testing.T) {
	binary := inspectionFixture(t, `
calls="$(dirname "$0")/calls"
if [ ! -f "$calls" ]; then
 printf '1' > "$calls"
 printf '{"nftables":[]}'
 exec sleep 10
fi
printf '2' > "$calls"
printf '%s' '{"nftables":[{"rule":{"family":"inet","table":"foreign","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":53}},{"redirect":{"port":5353}}]}}]}'
`)
	conflict, err := inspectDNSFirewall(t.Context(), binary)
	if err != nil || !conflict {
		t.Fatalf("busy boot permanently disabled inspection or trusted aborted clear snapshot: conflict=%v err=%v", conflict, err)
	}
	calls, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "calls"))
	if err != nil || strings.TrimSpace(string(calls)) != "2" {
		t.Fatalf("complete inspection was not repeated: calls=%q err=%v", calls, err)
	}
}
