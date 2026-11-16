package releaseupdate

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func installedAPKFixture(t *testing.T, replies map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Executable shell fixture requires Unix")
	}
	root := t.TempDir()
	for name, body := range replies {
		if err := os.WriteFile(filepath.Join(root, name+".json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
[ "$#" = 7 ] && [ "$1" = query ] && [ "$2" = --fields ] && [ "$3" = name,version,arch ] && [ "$4" = --format ] && [ "$5" = json ] && [ "$6" = --installed ] || exit 9
printf '%s\n' "$7" >> "$APK_TEST_ROOT/calls"
case "$7" in xroute-openwrt|sb-gateway-openwrt) ;; *) exit 8;; esac
[ -f "$APK_TEST_ROOT/$7.json" ] || exit 1
exec /bin/cat "$APK_TEST_ROOT/$7.json"
`
	if err := os.WriteFile(filepath.Join(root, "apk"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APK_TEST_ROOT", root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	return root
}

func TestReadInstalledUsesCanonicalPackageDatabaseEntry(t *testing.T) {
	root := installedAPKFixture(t, map[string]string{"xroute-openwrt": `[{"name":"xroute-openwrt","version":"0.1.23","arch":"aarch64_cortex-a53"}]`, "sb-gateway-openwrt": `[]`})
	got, err := ReadInstalled(t.Context())
	if err != nil || got != (Installed{Name: "xroute-openwrt", Version: "0.1.23", Arch: "aarch64_cortex-a53"}) {
		t.Fatal("Renamed installed package was not discovered", got, err)
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil || string(calls) != "xroute-openwrt\n" {
		t.Fatal("Canonical entry was not preferred", string(calls), err)
	}
}

func TestReadInstalledLegacyFallbackAndMetadataValidation(t *testing.T) {
	canonical := `[{"name":"xroute-openwrt","version":"0.1.24","arch":"x86_64"}]`
	legacy := `[{"name":"sb-gateway-openwrt","version":"0.1.21-r6","arch":"aarch64_generic"}]`
	for _, tc := range []struct {
		name, current, old string
		want               Installed
		failure            bool
	}{
		{"both-installed", canonical, legacy, Installed{"xroute-openwrt", "0.1.24", "x86_64"}, false},
		{"empty-current", "[]", legacy, Installed{"sb-gateway-openwrt", "0.1.21-r6", "aarch64_generic"}, false},
		{"missing-current", "", legacy, Installed{"sb-gateway-openwrt", "0.1.21-r6", "aarch64_generic"}, false},
		{"none", "[]", "[]", Installed{}, true},
		{"malformed", "not-json", legacy, Installed{}, true},
		{"unrelated-provider", `[{"name":"other","version":"1","arch":"x86_64"}]`, legacy, Installed{}, true},
		{"multiple", `[{"name":"xroute-openwrt"},{"name":"other"}]`, legacy, Installed{}, true},
		{"incomplete", `[{"name":"xroute-openwrt","version":"0.1.23"}]`, legacy, Installed{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replies := map[string]string{"sb-gateway-openwrt": tc.old}
			if tc.current != "" {
				replies["xroute-openwrt"] = tc.current
			}
			installedAPKFixture(t, replies)
			got, err := ReadInstalled(t.Context())
			if tc.failure {
				if err == nil {
					t.Fatal("Invalid installed metadata accepted", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatal(got, err)
			}
		})
	}
}

func TestReadInstalledHonorsCancellationBeforeQuery(t *testing.T) {
	root := installedAPKFixture(t, map[string]string{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadInstalled(ctx); err == nil {
		t.Fatal("Cancelled query accepted")
	}
	if calls, _ := os.ReadFile(filepath.Join(root, "calls")); strings.TrimSpace(string(calls)) != "" {
		t.Fatal("APK executed after cancellation")
	}
}
