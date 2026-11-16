package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCaptureCompatibilityChecksFeaturesBeforeInstallingAnyRules(t *testing.T) {
	for _, tc := range []struct {
		name, missing, fail string
		want                bool
	}{
		{"OpenWrt 25.12.6", "", "", true},
		{"GL.iNet with firewall3", "fw4", "", false},
		{"nft without TProxy", "", "tproxy_socket", false},
		{"firewall stopped", "", "active_firewall4", false},
		{"invalid existing firewall", "", "firewall_config", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commands := 0
			report := inspectCaptureCompatibility(t.Context(), compatibilityOps{
				release: []byte("DISTRIB_RELEASE='25.12.6'\n"),
				lookup: func(name string) (string, error) {
					if name == tc.missing || name == "opkg" {
						return "", errors.New("missing")
					}
					return "/test/" + name, nil
				},
				run: func(_ context.Context, binary string, args []string, input []byte) ([]byte, error) {
					commands++
					kind := "firewall_config"
					if strings.HasSuffix(binary, "/nft") {
						if len(input) == 0 {
							kind = "active_firewall4"
						} else {
							kind = "tproxy_socket"
							if strings.Join(args, " ") != "-c -f -" || !strings.Contains(string(input), "socket transparent 1") || !strings.Contains(string(input), "tproxy to") {
								t.Fatal("capability test could modify firewall or missed required features")
							}
						}
					} else if strings.Join(args, " ") != "check" {
						t.Fatal("compatibility check changes firewall")
					}
					if tc.fail == kind {
						return nil, errors.New("unsupported fixture")
					}
					return []byte("table inet fw4\n"), nil
				},
			})
			if report.Supported != tc.want || report.OpenWrt != "25.12.6" || report.PackageManager != "apk" {
				t.Fatalf("incorrect compatibility report: %+v", report)
			}
			if tc.missing != "" && commands != 0 {
				t.Fatal("missing dependencies did not stop preflight")
			}
		})
	}
}
