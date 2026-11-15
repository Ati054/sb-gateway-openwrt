package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func inspectionFixture(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("OpenWrt shell fixture")
	}
	path := filepath.Join(t.TempDir(), "nft")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFirewallInspectionDoesNotEnumerateSetContents(t *testing.T) {
	// A full listing of the same firewall would spend its deadline serializing
	// a large routing set. The compact listing still includes DNS port sets.
	binary := inspectionFixture(t, `
case " $* " in *" -n "*) ;; *) exit 11;; esac
case " $* " in *" -s "*) ;; *) exit 12;; esac
case " $* " in *" -t "*) ;; *) sleep 10; exit 13;; esac
cat <<'RULES'
{"nftables":[{"set":{"family":"inet","table":"foreign","name":"large_routes","type":"ipv4_addr"}},{"rule":{"family":"inet","table":"foreign","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":{"set":[53,853]}}},{"redirect":{"port":5353}}]}}]}
RULES
`)
	rules, err := inspectNFTRules(context.Background(), binary)
	conflict, parseErr := dnsFirewallConflictJSON([]byte(rules), nil)
	if err != nil || parseErr != nil || !conflict {
		t.Fatalf("compact snapshot lost a DNS conflict: %v, %s", err, rules)
	}
}

func TestFirewallInspectionRejectsFailedPartialSnapshot(t *testing.T) {
	binary := inspectionFixture(t, "printf 'table inet fw4 {\\n'\nprintf 'permission denied\\n' >&2\nexit 1\n")
	rules, err := inspectNFTRules(context.Background(), binary)
	if err == nil || rules != "" || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("failed snapshot accepted or diagnostic lost: %q, %v", rules, err)
	}
}

func TestFirewallInspectionRejectsOversizedSnapshot(t *testing.T) {
	binary := inspectionFixture(t, "dd if=/dev/zero bs=4096 count=1025 2>/dev/null\n")
	rules, err := inspectNFTRules(context.Background(), binary)
	if err == nil || rules != "" || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("truncated snapshot accepted: length=%d, %v", len(rules), err)
	}
}

func TestFirewallInspectionCancellationRetainsExistingDNS(t *testing.T) {
	binary := inspectionFixture(t, "sleep 10\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	state := resolveDNSInterop(ctx, "proxy-only", func(ctx context.Context) (bool, error) {
		_, err := inspectNFTRules(ctx, binary)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("deadline cause lost: %v", err)
		}
		return false, err
	}, func(context.Context) (string, error) { return "", nil })
	if state.EffectiveMode != "system" || state.Reason != "firewall-inspection-failed" {
		t.Fatalf("inspection cancellation took over DNS: %+v", state)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("inspection child or output pipe was not joined promptly")
	}
}

func TestFirewallInspectionOutputBoundsDiagnostics(t *testing.T) {
	output := nftInspectionOutput{limit: 8}
	if n, err := output.Write([]byte("123456789")); n != 9 || err != nil {
		t.Fatalf("subprocess stream was not drained: %d, %v", n, err)
	}
	if output.String() != "12345678" || !output.truncated {
		t.Fatalf("unbounded diagnostic: %+v", output)
	}
}
