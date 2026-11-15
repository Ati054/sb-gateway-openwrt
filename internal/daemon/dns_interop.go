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
	state := DNSInterop{RequestedMode: requested, EffectiveMode: requested}
	if requested == "system" {
		return state
	}
	// An external resolver may own dnsmasq's upstream even without a port-53
	// redirect. Keep DNS ownership exclusive; never change its configuration.
	service, inspectionErr := externalDNSService(ctx)
	if inspectionErr != nil {
		state.EffectiveMode = "system"
		state.ConflictingService = service
		state.Reason = "dns-service-inspection-failed"
		log.Printf("resproxy: DNS service inspection failed; existing DNS retained: %v", inspectionErr)
		return state
	}
	if service != "" {
		state.EffectiveMode = "system"
		state.ConflictingService = service
		if service == "https-dns-proxy" {
			state.Reason = "https-dns-proxy-running"
		} else {
			state.Reason = "other-dns-service"
		}
		return state
	}
	conflict, err := inspectFirewall(ctx)
	if err != nil {
		state.EffectiveMode = "system"
		state.Reason = "firewall-inspection-failed"
		log.Printf("resproxy: firewall inspection failed; existing DNS retained: %v", err)
		return state
	}
	if conflict {
		state.EffectiveMode = "system"
		state.Reason = "other-dns-redirect"
	}
	return state
}

// A stopped external owner must be restored before coexistence is usable.
// Its firewall reload may lag behind stop, so settle that transition first.
func settleDNSInterop(ctx context.Context, requested string, handedOff bool, inspectFirewall func(context.Context) (bool, error), externalDNSService func(context.Context) (string, error), restore func() error) (DNSInterop, error) {
	state := resolveDNSInterop(ctx, requested, inspectFirewall, externalDNSService)
handoffFirewallWait:
	for attempts := 0; handedOff && attempts < 10 && state.Reason == "other-dns-redirect"; attempts++ {
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			break handoffFirewallWait
		case <-timer.C:
		}
		state = resolveDNSInterop(ctx, requested, inspectFirewall, externalDNSService)
	}
	if handedOff && state.EffectiveMode == "system" {
		if err := restore(); err != nil {
			return state, errors.Join(ctx.Err(), fmt.Errorf("restore previous DNS owner after conflict: %w", err))
		}
	}
	return state, ctx.Err()
}

func activeExternalDNSService() string {
	service, _ := activeExternalDNSServiceWithContext(context.Background())
	return service
}

func activeExternalDNSServiceWithContext(ctx context.Context) (string, error) {
	return inspectExternalDNSServiceAt(ctx, "/etc/init.d")
}

func inspectExternalDNSServiceAt(ctx context.Context, initDir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
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
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
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
