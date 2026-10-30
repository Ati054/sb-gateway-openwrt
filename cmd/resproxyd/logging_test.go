package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	clog "github.com/xtls/xray-core/common/log"
)

func TestControllerStartupDoesNotEnableUpstreamDebugLogging(t *testing.T) {
	if os.Getenv("RESPROXY_TEST_LOG_DRIVER") == "1" {
		os.Args = []string{"resproxyd", "version"}
		main()
		clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Debug, Content: "DEBUG_SPAM_FIXTURE"})
		clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Warning, Content: "WARNING_KEEP_FIXTURE"})
		// The upstream console logger drains asynchronously.
		time.Sleep(200 * time.Millisecond)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestControllerStartupDoesNotEnableUpstreamDebugLogging$")
	cmd.Env = append(os.Environ(), "RESPROXY_TEST_LOG_DRIVER=1")
	body, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("controller logging fixture: %v: %s", err, body)
	}
	if strings.Contains(string(body), "DEBUG_SPAM_FIXTURE") || !strings.Contains(string(body), "WARNING_KEEP_FIXTURE") {
		t.Fatalf("startup logging lost warnings or kept upstream debug spam: %s", body)
	}
}
