//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInstallerFetchReportsDownloadFailure(t *testing.T) {
	body, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	start := strings.Index(source, "fetch() {")
	end := strings.Index(source, "\necho 'Checking release...'")
	if start < 0 || end <= start {
		t.Fatal("installer download function not found")
	}
	for _, tc := range []struct {
		name, payload, diagnostic, want string
		status, limit                   int
	}{
		{"success", "metadata", "download progress", "", 0, 64},
		{"HTTP failure", "", "HTTP error 403: API rate limit exceeded", "API rate limit exceeded", 8, 64},
		{"TLS failure", "", "Failed to verify server certificate", "Failed to verify server certificate", 5, 64},
		{"empty response", "", "", "exit 0", 0, 64},
		{"oversized response", "123456789", "", "Download exceeds the size limit", 0, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "payload"), []byte(tc.payload), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			mock := `#!/bin/sh
set -eu
 quiet=0
 for arg in "$@"; do [ "$arg" != '-q' ] || quiet=1; done
 [ "$quiet" = 1 ] || printf '%s\n' "$MOCK_DIAGNOSTIC" >&2
 cat "$MOCK_WORK/payload"
 exit "$MOCK_STATUS"
`
			if err := os.WriteFile(filepath.Join(dir, "bin", "uclient-fetch"), []byte(mock), 0700); err != nil {
				t.Fatal(err)
			}
			script := `set -eu
work=$1
fetch_pid=
PATH="$work/bin:$PATH"
export PATH
` + source[start:end] + `
fetch https://api.github.com/example "$work/result" "$2"
`
			path := filepath.Join(dir, "test.sh")
			if err := os.WriteFile(path, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", path, dir, strconv.Itoa(tc.limit))
			cmd.Env = append(os.Environ(), "MOCK_WORK="+dir, "MOCK_STATUS="+strconv.Itoa(tc.status), "MOCK_DIAGNOSTIC="+tc.diagnostic)
			out, err := cmd.CombinedOutput()
			if tc.want == "" {
				if err != nil || len(out) != 0 {
					t.Fatalf("successful download: %v, output %q", err, out)
				}
				got, err := os.ReadFile(filepath.Join(dir, "result"))
				if err != nil || string(got) != tc.payload {
					t.Fatalf("downloaded payload: %q, %v", got, err)
				}
			} else if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("want failure with %q, got %v: %s", tc.want, err, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "download.pipe")); !os.IsNotExist(err) {
				t.Fatalf("download FIFO was not removed: %v", err)
			}
		})
	}
}
