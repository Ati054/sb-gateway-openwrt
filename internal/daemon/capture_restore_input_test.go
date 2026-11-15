package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Ati054/xroute-openwrt/internal/capture"
	"github.com/Ati054/xroute-openwrt/internal/settings"
	"github.com/Ati054/xroute-openwrt/internal/xray"
)

func captureInputFixture() settings.Config {
	c := liveFixture()
	c.LANInterface = "br-lan"
	c.DNSMode = "proxy-only"
	c.DNSPort = 1053
	c.Base.Policies = []xray.BasePolicy{{ID: "main"}}
	c.Base.DefaultPolicy = "main"
	c.Capture.IPv6Mode = "proxy"
	c.Capture.DirectIPs = []string{"203.0.113.1/32"}
	return c
}

func TestCaptureRestoreInputPreservesSnapshotWithoutSourceCredentials(t *testing.T) {
	old, next := captureInputFixture(), captureInputFixture()
	old.Subscriptions[0].URL = "https://source.invalid/private-token-not-for-firewall"
	next.Capture.DirectIPs = []string{"198.51.100.5/32"}
	old.Capture.Automatic = capture.Inventory{Complete: true, Interfaces: []string{"stale-vpn"}}
	for _, config := range []settings.Config{old, next} {
		body, err := EncodeCaptureRestore(config)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte("private-token")) || bytes.Contains(body, []byte("stale-vpn")) {
			t.Fatal("non-capture data leaked into renderer input")
		}
		decoded, err := DecodeCaptureRestore(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded.Config().Capture.DirectIPs, config.Capture.DirectIPs) {
			t.Fatal("supplied capture generation lost")
		}
		if decoded.Policy.Automatic.Complete || len(decoded.Policy.Automatic.Interfaces) != 0 {
			t.Fatal("stale discovery reused")
		}
		if len(decoded.Scopes) != 1 || decoded.Scopes[0].AllowWAN {
			t.Fatal("strict fallback changed")
		}
	}
}

func TestCaptureRestoreInputRejectsOversizeUnknownAndTrailingDocuments(t *testing.T) {
	for _, body := range []string{strings.Repeat(" ", (1<<20)+1), `{"unexpected":"value"}`, `{} {}`, `{"policy":{"unknown":true}}`, "{"} {
		if _, err := DecodeCaptureRestore(strings.NewReader(body)); err == nil {
			t.Fatal("invalid renderer input accepted")
		}
	}
}

func TestCaptureRestoreSnapshotCannotCrossDaemonOwnership(t *testing.T) {
	paths := Paths{RunRoot: t.TempDir()}
	input := CaptureRestoreInput{OwnerPID: 123}
	if CaptureRestoreOwnerCurrent(input, paths) {
		t.Fatal("missing owner accepted")
	}
	path := filepath.Join(paths.RunRoot, "daemon.pid")
	if err := os.WriteFile(path, []byte("123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !CaptureRestoreOwnerCurrent(input, paths) {
		t.Fatal("current owner rejected")
	}
	if err := os.WriteFile(path, []byte("456\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if CaptureRestoreOwnerCurrent(input, paths) {
		t.Fatal("old daemon can overwrite new capture")
	}
}

func TestCaptureRecoveryUsesAcknowledgedConfigAfterFailedApply(t *testing.T) {
	previous, next := captureInputFixture(), captureInputFixture()
	next.Capture.DirectIPs = []string{"198.51.100.5/32"}
	c := newSubscriptionControl(previous)
	reads := 0
	c.readConfig = func(context.Context) (settings.Config, error) { reads++; return next, nil }
	c.applyReload = func(context.Context, settings.Config, settings.Config) error { return errors.New("pool failed") }
	c.restoreCapture = func(_ context.Context, config settings.Config) error {
		if !reflect.DeepEqual(config, previous) {
			t.Fatal("failed UCI edit reintroduced during recovery")
		}
		return nil
	}
	if result, _ := c.handle(reloadRequest{ctx: t.Context()}); result.Status != "error" {
		t.Fatal(result)
	}
	if result, _ := c.handle(reloadRequest{operation: "capture-restore", ctx: t.Context()}); result.Status != "applied" {
		t.Fatal(result)
	}
	if reads != 1 {
		t.Fatal("recovery reread unacknowledged UCI")
	}
}

func TestCaptureRecoverySerializesWithSuccessfulOrFailedApply(t *testing.T) {
	for _, failed := range []bool{false, true} {
		previous, next := captureInputFixture(), captureInputFixture()
		next.Capture.DirectIPs = []string{"198.51.100.5/32"}
		c := newSubscriptionControl(previous)
		c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
		entered, resume := make(chan struct{}), make(chan struct{})
		c.applyReload = func(context.Context, settings.Config, settings.Config) error {
			close(entered)
			<-resume
			if failed {
				return errors.New("failed")
			}
			return nil
		}
		want := next
		if failed {
			want = previous
		}
		c.restoreCapture = func(_ context.Context, config settings.Config) error {
			if !reflect.DeepEqual(config, want) {
				t.Error("recovery used mixed control generation")
			}
			return nil
		}
		applied, recovered := make(chan struct{}), make(chan error, 1)
		go func() { c.handle(reloadRequest{ctx: t.Context()}); close(applied) }()
		<-entered
		go func() { recovered <- c.restoreAcknowledgedCapture(t.Context()) }()
		close(resume)
		<-applied
		if err := <-recovered; err != nil {
			t.Fatal(err)
		}
	}
}
