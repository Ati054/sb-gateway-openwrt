package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFirewallPortSetTimeoutRestartsWholeSnapshot(t *testing.T) {
	binary := inspectionFixture(t, `
calls="$(dirname "$0")/calls"
case " $* " in
 *" -f - "*) printf '1' > "$(dirname "$0")/portloads"; exec sleep 10;;
esac
if [ ! -f "$calls" ]; then
 printf '1' > "$calls"
 printf '%s' '{"nftables":[{"set":{"family":"inet","table":"foreign","name":"ports","type":"inet_service"}},{"rule":{"family":"inet","table":"foreign","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":"@ports"}},{"redirect":{"port":5353}}]}}]}'
else
 printf '2' > "$calls"
 printf '%s' '{"nftables":[{"rule":{"family":"inet","table":"foreign","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":53}},{"redirect":{"port":5353}}]}}]}'
fi
`)
	conflict, err := inspectDNSFirewallAttempts(t.Context(), binary, []time.Duration{100 * time.Millisecond, 2 * time.Second})
	if err != nil || !conflict {
		t.Fatalf("port-set timeout did not restart the structural snapshot: %v, %v", conflict, err)
	}
	for name, want := range map[string]string{"calls": "2", "portloads": "1"} {
		got, err := os.ReadFile(filepath.Join(filepath.Dir(binary), name))
		if err != nil || string(got) != want {
			t.Fatalf("%s=%q, want %s: %v", name, got, want, err)
		}
	}
}

func TestFirewallInspectionDoesNotRetryPermanentFailure(t *testing.T) {
	binary := inspectionFixture(t, `
printf 'x' >> "$(dirname "$0")/calls"
printf '{"nftables":[]}'
printf 'permission denied' >&2
exit 7
`)
	conflict, err := inspectDNSFirewallAttempts(t.Context(), binary, []time.Duration{time.Second, time.Second})
	if err == nil || conflict || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("permanent failure accepted or lost: %v, %v", conflict, err)
	}
	calls, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "calls"))
	if err != nil || string(calls) != "x" {
		t.Fatalf("permanent failure retried: %q, %v", calls, err)
	}
}

func TestFirewallInspectionParentCancellationStopsRetries(t *testing.T) {
	binary := inspectionFixture(t, `printf 'x' >> "$(dirname "$0")/calls"; exec sleep 10`)
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := inspectDNSFirewallAttempts(ctx, binary, []time.Duration{time.Second, 2 * time.Second})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("parent cancellation lost or retried: %v", err)
	}
	if calls, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "calls")); err == nil && string(calls) != "x" {
		t.Fatalf("canceled inspection retried: %q", calls)
	}
}

func TestDNSServiceInspectionRetriesTransientTimeout(t *testing.T) {
	dir := t.TempDir()
	fixture := inspectionFixture(t, `
calls="$(dirname "$0")/calls"
if [ ! -f "$calls" ]; then printf '1' > "$calls"; exec sleep 10; fi
printf '2' > "$calls"
exit 1
`)
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "https-dns-proxy"), body, 0700); err != nil {
		t.Fatal(err)
	}
	service, err := inspectExternalDNSServiceAttempts(t.Context(), dir, []time.Duration{100 * time.Millisecond, 2 * time.Second})
	if err != nil || service != "" {
		t.Fatalf("transient service probe became permanent coexistence: %q, %v", service, err)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil || string(calls) != "2" {
		t.Fatalf("service state was not inspected again: %q, %v", calls, err)
	}
}

func TestExhaustedTransientDNSInspectionDoesNotBecomePermanentSystemMode(t *testing.T) {
	for _, handedOff := range []bool{false, true} {
		restored := 0
		state, err := settleDNSInterop(t.Context(), "proxy-only", handedOff,
			func(context.Context) (bool, error) { return false, context.DeadlineExceeded },
			func(context.Context) (string, error) { return "", nil },
			func() error { restored++; return nil })
		if !errors.Is(err, context.DeadlineExceeded) || dnsInteropReady(state) || restored != map[bool]int{false: 0, true: 1}[handedOff] {
			t.Fatalf("transient inspection was accepted as lasting coexistence: handedOff=%v state=%+v restored=%d err=%v", handedOff, state, restored, err)
		}
	}
}
