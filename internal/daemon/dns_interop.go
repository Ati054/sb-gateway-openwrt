package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DNSInterop records what actually happened, rather than presenting an opted-in
// DNS mode as active when another resolver already owns LAN DNS interception.
type DNSInterop struct {
	RequestedMode      string `json:"requested_mode"`
	EffectiveMode      string `json:"effective_mode"`
	Reason             string `json:"reason,omitempty"`
	ConflictingService string `json:"conflicting_service,omitempty"`
	HandoffConfirmed   bool   `json:"handoff_confirmed,omitempty"`
}

func dnsInteropPath(paths Paths) string {
	return filepath.Join(paths.RunRoot, "dns-interop.json")
}

func publishDNSInterop(paths Paths, state DNSInterop) error {
	content, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(dnsInteropPath(paths), append(content, '\n'), 0o600)
}

func dnsInteropReady(state DNSInterop) bool {
	if state.RequestedMode == "system" || state.EffectiveMode == "off" || state.EffectiveMode == state.RequestedMode {
		return true
	}
	if state.EffectiveMode == "system" {
		switch state.Reason {
		case "https-dns-proxy-running", "other-dns-service", "other-dns-redirect":
			return true
		}
	}
	return false
}

func hotRuntimeReadyPath(paths Paths, state DNSInterop) string {
	name := "hot-ready.json"
	if !dnsInteropReady(state) {
		// Routing may continue with the previous system resolver. Older APK
		// update helpers must not mistake that degraded state for full readiness.
		name = "routes-ready.json"
	}
	return filepath.Join(paths.RunRoot, name)
}

func resolveDNSInterop(ctx context.Context, requested string, inspectFirewall func(context.Context) (bool, error), externalDNSService func(context.Context) (string, error)) DNSInterop {
	state, _ := resolveDNSInteropResult(ctx, requested, inspectFirewall, externalDNSService)
	return state
}

func resolveDNSInteropResult(ctx context.Context, requested string, inspectFirewall func(context.Context) (bool, error), externalDNSService func(context.Context) (string, error)) (DNSInterop, error) {
	state := DNSInterop{RequestedMode: requested, EffectiveMode: requested}
	if requested == "system" {
		return state, nil
	}
	// An external resolver may own dnsmasq's upstream even without a port-53
	// redirect. Keep DNS ownership exclusive; never change its configuration.
	service, inspectionErr := externalDNSService(ctx)
	if inspectionErr != nil {
		state.EffectiveMode = "system"
		state.ConflictingService = service
		state.Reason = "dns-service-inspection-failed"
		log.Printf("resproxy: DNS service inspection failed; existing DNS retained: %v", inspectionErr)
		return state, inspectionErr
	}
	if service != "" {
		state.EffectiveMode = "system"
		state.ConflictingService = service
		if service == "https-dns-proxy" {
			state.Reason = "https-dns-proxy-running"
		} else {
			state.Reason = "other-dns-service"
		}
		return state, nil
	}
	conflict, err := inspectFirewall(ctx)
	if err != nil {
		state.EffectiveMode = "system"
		state.Reason = "firewall-inspection-failed"
		log.Printf("resproxy: firewall inspection failed; existing DNS retained: %v", err)
		return state, err
	}
	if conflict {
		state.EffectiveMode = "system"
		state.Reason = "other-dns-redirect"
	}
	return state, nil
}

// A stopped external owner must be restored before coexistence is usable.
// Its firewall reload may lag behind stop, so settle that transition first.
func settleDNSInterop(ctx context.Context, requested string, handedOff bool, inspectFirewall func(context.Context) (bool, error), externalDNSService func(context.Context) (string, error), restore func() error) (DNSInterop, error) {
	state, inspectionErr := resolveDNSInteropResult(ctx, requested, inspectFirewall, externalDNSService)
handoffFirewallWait:
	for attempts := 0; handedOff && attempts < 10 && state.Reason == "other-dns-redirect"; attempts++ {
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			break handoffFirewallWait
		case <-timer.C:
		}
		state, inspectionErr = resolveDNSInteropResult(ctx, requested, inspectFirewall, externalDNSService)
	}
	if handedOff && state.EffectiveMode == "system" {
		if err := restore(); err != nil {
			return state, errors.Join(ctx.Err(), inspectionErr, fmt.Errorf("restore previous DNS owner after conflict: %w", err))
		}
	}
	if err := retryDNSStartupFailure(ctx, "transient inspection failure", inspectionErr); err != nil {
		// Do not turn exhausted transient startup checks into a permanent
		// system-DNS mode. The previous owner is restored above; returning a
		// startup error lets procd retry initialization under its existing
		// supervision, without ever publishing false full readiness.
		return state, err
	}
	return state, nil
}

// A temporary local startup deadline must not latch a degraded DNS mode until a
// manual restart. Call after restoring any DNS ownership already acquired.
// External resolver timeouts are not evidence that the local runtime is broken.
func retryDNSStartupFailure(ctx context.Context, stage string, cause error) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(err, cause)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return fmt.Errorf("retry DNS initialization after %s: %w", stage, cause)
	}
	return nil
}

func activeExternalDNSService() string {
	// Status requests must not inherit startup retries: LuCI polls this path.
	service, _ := inspectExternalDNSServiceAttempts(context.Background(), "/etc/init.d", []time.Duration{2 * time.Second})
	return service
}

func activeExternalDNSServiceWithContext(ctx context.Context) (string, error) {
	return inspectExternalDNSServiceAt(ctx, "/etc/init.d")
}

func inspectExternalDNSServiceAt(ctx context.Context, initDir string) (string, error) {
	return inspectExternalDNSServiceAttempts(ctx, initDir, dnsInspectionBudgets())
}

func inspectExternalDNSServiceAttempts(ctx context.Context, initDir string, budgets []time.Duration) (string, error) {
	return retryDNSInspection(ctx, "DNS service", budgets, func(ctx context.Context, budget time.Duration) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return externalDNSServiceAt(initDir, func(path string) (bool, error) {
			err := boundedCommand(ctx, path, "running").Run()
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if err == nil {
				return true, nil
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				return false, nil
			}
			return false, err
		})
	})
}

func dnsInspectionBudgets() []time.Duration {
	return []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
}

func retryDNSInspection[T any](ctx context.Context, label string, budgets []time.Duration, inspect func(context.Context, time.Duration) (T, error)) (T, error) {
	var value T
	var last error
	if len(budgets) == 0 {
		return value, errors.New("DNS inspection requires an attempt budget")
	}
	for attempt, budget := range budgets {
		if err := ctx.Err(); err != nil {
			return value, err
		}
		var err error
		value, err = inspect(ctx, budget)
		if err == nil {
			return value, nil
		}
		if err := ctx.Err(); err != nil {
			return value, err
		}
		// Retry only an aborted local inspection. A busy boot is not proof
		// of a permanent DNS conflict. Each attempt starts from scratch;
		// failed output and port-set caches must never cross this boundary.
		// Permission, syntax and structural errors remain final failures.
		if !errors.Is(err, context.DeadlineExceeded) {
			return value, err
		}
		last = err
		if attempt+1 < len(budgets) {
			log.Printf("resproxy: %s inspection timed out; retrying complete inspection (attempt %d/%d)", label, attempt+2, len(budgets))
		}
	}
	return value, fmt.Errorf("%s inspection exhausted %d attempts: %w", label, len(budgets), last)
}

func ActiveExternalDNSService() string {
	return activeExternalDNSService()
}

func externalDNSServiceAt(initDir string, running func(string) (bool, error)) (string, error) {
	for _, service := range []string{
		"https-dns-proxy", "smartdns", "mosdns", "adguardhome",
		"AdGuardHome", "unbound", "dnscrypt-proxy", "dnscrypt-proxy2",
	} {
		path := filepath.Join(initDir, service)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return service, err
		}
		active, err := running(path)
		if err != nil || active {
			return service, err
		}
	}
	return "", nil
}

func inspectNFTRules(ctx context.Context, binary string) (string, error) {
	return readNFTInspection(ctx, binary, nil, "-n", "-s", "-t", "-j", "list", "ruleset")
}

// Drain bounded output; callers accept only complete successful snapshots.
func readNFTInspection(ctx context.Context, binary string, input io.Reader, args ...string) (string, error) {
	return readNFTInspectionWithin(ctx, binary, input, 2*time.Second, args...)
}

func readNFTInspectionWithin(ctx context.Context, binary string, input io.Reader, budget time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	started := time.Now()
	output := nftInspectionOutput{limit: 4 << 20}
	diagnostic := nftInspectionOutput{limit: 4096}
	command := boundedCommand(ctx, binary, args...)
	command.Stdin = input
	command.Stdout = &output
	command.Stderr = &diagnostic
	err := command.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("inspect nftables after %s: %w; %s", time.Since(started).Round(time.Millisecond), err, strings.Join(strings.Fields(diagnostic.String()), " "))
	}
	if output.truncated {
		return "", fmt.Errorf("inspect nftables: rule structure exceeds %d bytes", output.limit)
	}
	return output.String(), nil
}

// Keep draining both streams so a verbose subprocess cannot deadlock. An
// incomplete stdout is never accepted as proof that no conflict exists.
type nftInspectionOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (output *nftInspectionOutput) Write(body []byte) (int, error) {
	keep := len(body)
	if remaining := output.limit - output.buffer.Len(); keep > remaining {
		keep = remaining
		output.truncated = true
	}
	_, _ = output.buffer.Write(body[:keep])
	return len(body), nil
}

func (output *nftInspectionOutput) String() string {
	return output.buffer.String()
}
