package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type CompatibilityCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type CaptureCompatibility struct {
	OpenWrt          string               `json:"openwrt,omitempty"`
	PackageManager   string               `json:"package_manager,omitempty"`
	Supported        bool                 `json:"capture_supported"`
	Checks           []CompatibilityCheck `json:"checks"`
	BridgeAdjustment []string             `json:"bridge_netfilter_adjustment,omitempty"`
}

type compatibilityOps struct {
	lookup  func(string) (string, error)
	run     func(context.Context, string, []string, []byte) ([]byte, error)
	release []byte
}

// InspectCaptureCompatibility only checks transactions. It never installs
// tables, changes dnsmasq, replaces firmware feeds or disables other services.
func InspectCaptureCompatibility(ctx context.Context) CaptureCompatibility {
	body, _ := os.ReadFile("/etc/openwrt_release")
	report := inspectCaptureCompatibility(ctx, compatibilityOps{
		lookup: exec.LookPath, release: body,
		run: func(ctx context.Context, binary string, args []string, input []byte) ([]byte, error) {
			command := boundedCommand(ctx, binary, args...)
			command.Stdin = bytes.NewReader(input)
			return command.CombinedOutput()
		},
	})
	for _, name := range bridgeCaptureFlags {
		if value, err := bridgeFlag(bridgeSysctlRoot, name); err == nil && value == "1" {
			report.BridgeAdjustment = append(report.BridgeAdjustment, name)
		}
	}
	return report
}

func inspectCaptureCompatibility(ctx context.Context, ops compatibilityOps) CaptureCompatibility {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result := CaptureCompatibility{Supported: true}
	for _, line := range strings.Split(string(ops.release), "\n") {
		if value, ok := strings.CutPrefix(line, "DISTRIB_RELEASE="); ok {
			result.OpenWrt = strings.Trim(value, "'\"")
		}
	}
	for _, manager := range []string{"apk", "opkg"} {
		if _, err := ops.lookup(manager); err == nil {
			result.PackageManager = manager
			break
		}
	}
	add := func(name string, ok bool, detail string) {
		result.Checks = append(result.Checks, CompatibilityCheck{name, ok, detail})
		result.Supported = result.Supported && ok
	}
	paths := map[string]string{}
	for _, name := range []string{"fw4", "nft", "ip"} {
		path, err := ops.lookup(name)
		paths[name] = path
		detail := ""
		if err != nil {
			detail = "Required command is missing: " + name
		}
		add(name, err == nil, detail)
	}
	if !result.Supported {
		return result
	}
	for _, check := range []struct {
		name, binary string
		args         []string
		input        []byte
	}{
		{"firewall_config", paths["fw4"], []string{"check"}, nil},
		{"active_firewall4", paths["nft"], []string{"list", "tables"}, nil},
		{"tproxy_socket", paths["nft"], []string{"-c", "-f", "-"}, []byte("table ip xroute_probe_" + strconv.Itoa(os.Getpid()) + " {\n chain prerouting {\n type filter hook prerouting priority mangle; policy accept;\n meta l4proto tcp socket transparent 1 accept\n meta l4proto tcp tproxy to 127.0.0.1:12345 accept\n }\n}\n")},
	} {
		output, err := ops.run(ctx, check.binary, check.args, check.input)
		ok := err == nil
		if check.name == "active_firewall4" {
			ok = ok && strings.Contains("\n"+string(output)+"\n", "\ntable inet fw4\n")
		}
		detail := ""
		if !ok {
			detail = "Check failed: " + check.name
			if check.name == "tproxy_socket" && len(output) > 0 {
				diagnostic := strings.TrimSpace(string(output))
				if len(diagnostic) > 512 {
					diagnostic = diagnostic[:512]
				}
				detail += "; " + diagnostic
			}
			if ctx.Err() != nil {
				detail += " (timeout or cancellation)"
			}
		}
		add(check.name, ok, detail)
		if !ok {
			return result
		}
	}
	return result
}

func requireCaptureCompatibility(ctx context.Context) error {
	report := InspectCaptureCompatibility(ctx)
	if report.Supported {
		return nil
	}
	var failed []string
	for _, check := range report.Checks {
		if !check.OK {
			failed = append(failed, check.Detail)
		}
	}
	return fmt.Errorf("LAN capture is unavailable on this firmware: %s; run 'resproxyd doctor' for diagnostics", strings.Join(failed, "; "))
}
