//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func installerDownloadFunctions(t *testing.T) string {
	t.Helper()
	path := os.Getenv("RESPROXY_INSTALLER_TEST_SOURCE")
	if path == "" {
		path = "../../install.sh"
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	start, fetch := strings.Index(s, "check_ram() {"), strings.Index(s, "fetch() {")
	if start < 0 || fetch < start {
		t.Fatal("installer functions missing")
	}
	end := strings.Index(s[fetch:], "\n}\n")
	if end < 0 {
		t.Fatal("installer fetch is incomplete")
	}
	return s[start : fetch+end+len("\n}\n")]
}

func installerDownloaderFixture(t *testing.T, curlPresent bool, legacyStatus, updateStatus, addStatus, curlStatus int, oversize bool) (string, []byte, error, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mkfifo", "head", "wc", "rm", "cat", "awk", "df", "grep", "setsid", "cp", "chmod", "sleep"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := "#!/bin/sh\nprintf legacy >> \"$MOCK_WORK/legacy-calls\"\nprintf partial\nexit \"$MOCK_LEGACY_STATUS\"\n"
	curl := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$MOCK_WORK/curl-args\"\nprintf final-body\nexit \"$MOCK_CURL_STATUS\"\n"
	if oversize {
		curl = "#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$MOCK_WORK/writer-pid\"\nprintf 123456789\nexec sleep 30\n"
		legacy = curl
	}
	write(filepath.Join(bin, "uclient-fetch"), legacy)
	write(filepath.Join(dir, "curl-template"), curl)
	if curlPresent {
		write(filepath.Join(bin, "curl"), curl)
	}
	write(filepath.Join(bin, "apk"), `#!/bin/sh
printf '%s\n' "$*" >> "$MOCK_WORK/apk-calls"
case "$1" in
 update) exit "$MOCK_APK_UPDATE";;
 add)
  [ "$MOCK_APK_ADD" = 0 ] || exit "$MOCK_APK_ADD"
  cp "$MOCK_WORK/curl-template" "$MOCK_WORK/bin/curl"
  chmod 700 "$MOCK_WORK/bin/curl";;
 *) exit 98;;
esac
`)
	script := "set -eu\nwork=$1\nfetch_pid=\nsetup_pid=\nsetup_watchdog_pid=\nPATH=\"$work/bin\"\nexport PATH\n" + installerDownloadFunctions(t) + "\nfetch https://github.com/example \"$work/result\" \"$2\"\n"
	write(filepath.Join(dir, "test.sh"), script)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	limit := 64
	if oversize {
		limit = 8
	}
	cmd := exec.CommandContext(ctx, sh, filepath.Join(dir, "test.sh"), dir, strconv.Itoa(limit))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Env = append(os.Environ(), "MOCK_WORK="+dir, "MOCK_LEGACY_STATUS="+strconv.Itoa(legacyStatus), "MOCK_APK_UPDATE="+strconv.Itoa(updateStatus), "MOCK_APK_ADD="+strconv.Itoa(addStatus), "MOCK_CURL_STATUS="+strconv.Itoa(curlStatus))
	out, runErr := cmd.CombinedOutput()
	if _, err := os.Stat(filepath.Join(dir, "download.pipe")); !os.IsNotExist(err) {
		t.Fatalf("FIFO retained: %v", err)
	}
	return dir, out, runErr, ctx.Err()
}

func TestInstallerCurlPreferenceAndDependencyFallback(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		curl                            bool
		legacy, update, add, curlStatus int
		success                         bool
		calls                           string
	}{
		{"curl preferred", true, 4, 0, 0, 0, true, ""},
		{"working legacy needs no dependency", false, 0, 0, 0, 0, true, ""},
		{"failed legacy installs trusted curl once", false, 4, 0, 0, 0, true, "update\nadd --no-progress curl\n"},
		{"repository failure", false, 4, 7, 0, 0, false, "update\n"},
		{"dependency failure", false, 4, 0, 9, 0, false, "update\nadd --no-progress curl\n"},
		{"curl failure does not reinstall", true, 0, 0, 0, 22, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, out, err, ctxErr := installerDownloaderFixture(t, tc.curl, tc.legacy, tc.update, tc.add, tc.curlStatus, false)
			if ctxErr != nil || (err == nil) != tc.success {
				t.Fatalf("result err=%v context=%v output=%s", err, ctxErr, out)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "apk-calls"))
			if string(calls) != tc.calls {
				t.Fatalf("unexpected package operations: %q", calls)
			}
			result, readErr := os.ReadFile(filepath.Join(dir, "result"))
			if tc.success {
				want := "final-body"
				if !tc.curl && tc.legacy == 0 {
					want = "partial"
				}
				if readErr != nil || string(result) != want {
					t.Fatalf("partial response contaminated retry: %q %v", result, readErr)
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatalf("failed response retained: %q %v", result, readErr)
			}
			if tc.curl {
				if b, _ := os.ReadFile(filepath.Join(dir, "legacy-calls")); len(b) != 0 {
					t.Fatalf("curl did not take precedence: %q", b)
				}
			}
			if b, e := os.ReadFile(filepath.Join(dir, "curl-args")); e == nil {
				for _, flag := range []string{"-fLsS", "--connect-timeout 15", "--max-time 180", "--max-redirs 5", "--proto =https", "--proto-redir =https"} {
					if !strings.Contains(string(b), flag) {
						t.Fatalf("missing secure transfer constraint %q: %s", flag, b)
					}
				}
			}
		})
	}
}

func TestInstallerOversizedWriterIsKilledBeforeWait(t *testing.T) {
	dir, out, err, ctxErr := installerDownloaderFixture(t, true, 0, 0, 0, 0, true)
	if err == nil || ctxErr != nil || !strings.Contains(string(out), "Download exceeds the size limit") {
		t.Fatalf("oversized writer was not stopped before wait: err=%v context=%v output=%s", err, ctxErr, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "result")); !os.IsNotExist(err) {
		t.Fatalf("oversized body retained: %v", err)
	}
	pidRaw, err := os.ReadFile(filepath.Join(dir, "writer-pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("download process %d still alive", pid)
	}
}

func TestInstallerDependencyDeadlineWithoutTimeoutUtility(t *testing.T) {
	dir := t.TempDir()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(dir, "worker.sh")
	body := "#!/bin/sh\ntrap '' TERM\nprintf '%s\\n' \"$$\" > \"$1\"\nexec \"$2\" 30\n"
	if err := os.WriteFile(worker, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "test.sh")
	source := "set -eu\nsetup_pid=\nsetup_watchdog_pid=\n" + installerDownloadFunctions(t) + "\nrun_bounded 1 \"$1\" \"$2\" \"$3\" \"$4\"\n"
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, script, sh, worker, filepath.Join(dir, "pid"), sleep)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 500 * time.Millisecond
	start := time.Now()
	out, runErr := cmd.CombinedOutput()
	if runErr == nil || ctx.Err() != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("dependency deadline failed: err=%v ctx=%v output=%s", runErr, ctx.Err(), out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("dependency process %d survived deadline", pid)
	}
}

func TestInstallerDependencyCleanupStopsDescendants(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "deadline"
		if cancelled {
			name = "parent-cancelled"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			sh, err := exec.LookPath("sh")
			if err != nil {
				t.Fatal(err)
			}
			sleep, err := exec.LookPath("sleep")
			if err != nil {
				t.Fatal(err)
			}
			worker := filepath.Join(dir, "worker.sh")
			body := "#!/bin/sh\ntrap '' TERM\n\"$2\" 30 &\nchild=$!\nprintf '%s\\n' \"$child\" > \"$1\"\nwait \"$child\"\n"
			if err := os.WriteFile(worker, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(dir, "test.sh")
			source := "set -eu\nsetup_pid=\nsetup_watchdog_pid=\n" + installerDownloadFunctions(t) + "\ntrap stop_setup EXIT\ntrap 'exit 143' TERM\nrun_bounded \"$1\" \"$2\" \"$3\" \"$4\" \"$5\"\n"
			if err := os.WriteFile(script, []byte(source), 0700); err != nil {
				t.Fatal(err)
			}
			limit := "1"
			if cancelled {
				limit = "120"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, sh, script, limit, sh, worker, filepath.Join(dir, "child"), sleep)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			cmd.WaitDelay = 500 * time.Millisecond
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			for n := 0; n < 100; n++ {
				raw, err = os.ReadFile(filepath.Join(dir, "child"))
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Kill(pid, syscall.SIGKILL)
			if cancelled {
				if err := syscall.Kill(cmd.Process.Pid, syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			err = cmd.Wait()
			if err == nil || ctx.Err() != nil {
				t.Fatalf("bounded setup did not terminate: err=%v ctx=%v", err, ctx.Err())
			}
			for n := 0; n < 50; n++ {
				stat, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
				if readErr != nil || strings.Contains(string(stat), ") Z ") {
					break
				}
				if n == 49 {
					t.Fatalf("dependency child %d survived: %s", pid, stat)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
