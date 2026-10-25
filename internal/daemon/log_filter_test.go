package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJournalCategoriesSeparateControllerCoreAndSystem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenWrt shell helper")
	}
	root := t.TempDir()
	fixture := "Jan 1 daemon.err resproxyd[1]: agent: route-health switch\n" +
		"Jan 1 daemon.info resproxyd[1]: resproxy: configuration applied\n" +
		"Jan 1 daemon.info resproxyd[1]: [Warning] common/errors: The feature fixture transport is deprecated\n" +
		"Jan 2 daemon.info resproxyd[1]: [Warning] common/errors: The feature fixture transport is deprecated\n" +
		"Jan 3 daemon.info resproxyd[1]: [Warning] common/errors: The feature fixture transport is deprecated\n" +
		"Jan 1 daemon.info resproxyd[1]: [Error] transport/internet: connection failed\n" +
		"Jan 2 daemon.info resproxyd[1]: [Error] transport/internet: connection failed\n" +
		"Jan 1 daemon.info resproxyd[2]: [Info] transport/internet: connection ready\n" +
		"Jan 1 daemon.err resproxyd[1]: resproxyd: startup halted\n" +
		"Jan 1 daemon.info dnsmasq[3]: unrelated system message\n"
	if err := os.WriteFile(filepath.Join(root, "logread"), []byte("#!/bin/sh\ncat <<'JOURNAL'\n"+fixture+"JOURNAL\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, category := range []string{"events", "errors", "xray", "all"} {
		cmd := exec.Command("sh", "../../files/resproxy-log", category, "200")
		cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"))
		body, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(category, err, string(body))
		}
		for _, check := range []struct {
			text string
			want bool
		}{
			{"route-health switch", category == "events" || category == "all"},
			{"configuration applied", category == "events" || category == "all"},
			{"fixture transport is deprecated", category == "errors" || category == "xray" || category == "all"},
			{"connection ready", category == "xray" || category == "all"},
			{"startup halted", category == "events" || category == "errors" || category == "all"},
			{"unrelated system message", category == "all"},
		} {
			if strings.Contains(string(body), check.text) != check.want {
				t.Fatal("incorrect journal category", category, check.text, string(body))
			}
		}
		if category != "events" {
			if strings.Count(string(body), "fixture transport is deprecated") != 1 || strings.Count(string(body), "connection failed") != 2 {
				t.Fatal("deprecation collapse lost runtime errors or retained spam", category, string(body))
			}
		}
	}
}
