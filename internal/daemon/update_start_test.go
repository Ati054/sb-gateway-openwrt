package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAPKAutoStartDefersOnlyForLiveManagedInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenWrt init shell")
	}
	init, err := os.ReadFile("../../files/resproxy.init")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, phase, pid, command string
		busy                      bool
		want                      int
	}{
		{"managed-install", "installing", fmt.Sprint(os.Getpid()), "/bin/sh\x00/tmp/.sb-gateway-upgrade-runner\x00install\x00", true, 0},
		{"github-install", "installing", fmt.Sprint(os.Getpid()), "/bin/sh\x00/tmp/.sb-gateway-upgrade-runner\x00github-install-worker\x00", true, 0},
		{"activation", "activating", fmt.Sprint(os.Getpid()), "/bin/sh\x00/tmp/.sb-gateway-upgrade-runner\x00install\x00", true, 77},
		{"check", "installing", fmt.Sprint(os.Getpid()), "/bin/sh\x00/tmp/.sb-gateway-upgrade-runner\x00github-check-worker\x00", true, 77},
		{"no-lock", "installing", fmt.Sprint(os.Getpid()), "/bin/sh\x00/tmp/.sb-gateway-upgrade-runner\x00install\x00", false, 77},
		{"stale-pid", "installing", "99999999", "/bin/sh\x00/tmp/.sb-gateway-upgrade-runner\x00install\x00", true, 77},
		{"reused-pid", "installing", fmt.Sprint(os.Getpid()), "/bin/sh\x00unrelated-command\x00", true, 77},
		{"invalid-pid", "installing", "0", "", true, 77},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			state, proc := filepath.Join(root, "state"), filepath.Join(root, "proc")
			if err := os.MkdirAll(filepath.Join(proc, tc.pid), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			for path, content := range map[string]string{filepath.Join(state, "phase"): tc.phase, filepath.Join(state, "worker.pid"): tc.pid + "\n", filepath.Join(proc, tc.pid, "cmdline"): tc.command} {
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.busy {
				if err := os.WriteFile(filepath.Join(state, "busy"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			source := strings.NewReplacer(". /lib/functions.sh", "", "/var/run/sb-gateway-upgrade", state, "/proc/", proc+"/").Replace(string(init))
			script := "extra_command() { :; }\n" + source + "\nconfig_load() { exit 77; }\nstart_service\n"
			cmd := exec.Command("sh", "-s")
			cmd.Stdin = strings.NewReader(script)
			output, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					code = exit.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tc.want {
				t.Fatal("incorrect APK startup deferral", code, tc.want, string(output))
			}
		})
	}
}
