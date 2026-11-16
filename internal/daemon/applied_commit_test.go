package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/Ati054/xroute-openwrt/internal/settings"
)

func TestLiveRecoverySnapshotCommitsAfterAllRuntimePublications(t *testing.T) {
	paths := Paths{DataRoot: t.TempDir(), RunRoot: t.TempDir()}
	previous, next := captureInputFixture(), captureInputFixture()
	next.Capture.DirectIPs = []string{"198.51.100.1/32"}
	next.Policies[0].SelectionOrder = []string{"country:NL"}
	if err := saveAppliedNetwork(paths, previous); err != nil {
		t.Fatal(err)
	}
	c := newSubscriptionControl(previous)
	c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
	published := false
	c.applyReload = func(context.Context, settings.Config, settings.Config) error {
		applied, err := readAppliedNetwork(paths)
		if err != nil || !reflect.DeepEqual(applied.Capture.DirectIPs, previous.Capture.DirectIPs) {
			t.Fatal("uncommitted runtime edit replaced recovery", err)
		}
		published = true
		return nil
	}
	c.persistApplied = func(config settings.Config) error {
		if !published {
			t.Fatal("recovery saved before runtime publication")
		}
		return saveAppliedNetwork(paths, config)
	}
	if result, _ := c.handle(reloadRequest{ctx: t.Context()}); result.Status != "applied" {
		t.Fatal(result)
	}
	applied, err := readAppliedNetwork(paths)
	if err != nil || !reflect.DeepEqual(applied.Policies, next.Policies) || !reflect.DeepEqual(applied.Capture.DirectIPs, next.Capture.DirectIPs) {
		t.Fatal("acknowledged membership or capture missing from recovery", applied, err)
	}
}

func TestRecoveryPersistenceFailureRollsRuntimeBackWithoutAcknowledging(t *testing.T) {
	previous, next := captureInputFixture(), captureInputFixture()
	next.Capture.DirectIPs = []string{"198.51.100.1/32"}
	c := newSubscriptionControl(previous)
	c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
	runtime := previous
	c.applyReload = func(_ context.Context, _, config settings.Config) error { runtime = config; return nil }
	storageErr := errors.New("no space for recovery state")
	c.persistApplied = func(config settings.Config) error {
		if reflect.DeepEqual(config.Capture, next.Capture) {
			return storageErr
		}
		return nil
	}
	if result, _ := c.handle(reloadRequest{ctx: t.Context()}); result.Status != "error" {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(c.snapshot(), previous) || !reflect.DeepEqual(runtime, previous) {
		t.Fatal("failed persistence acknowledged or retained the new runtime")
	}
}

func TestConfigurationApplyWaitsForStartupBeforeReadingDesiredSettings(t *testing.T) {
	c := newSubscriptionControl(captureInputFixture())
	read := false
	c.beforeApply = func(context.Context) error { return context.DeadlineExceeded }
	c.readConfig = func(context.Context) (settings.Config, error) { read = true; return captureInputFixture(), nil }
	if result, _ := c.handle(reloadRequest{ctx: t.Context()}); result.Status != "error" || read {
		t.Fatal("startup accepted settings that its ready publication would overwrite", result)
	}
}

func TestRecoveryPersistenceAndRollbackFailureStopsUncertainRuntime(t *testing.T) {
	previous, next := captureInputFixture(), captureInputFixture()
	next.Capture.DirectIPs = []string{"198.51.100.1/32"}
	c := newSubscriptionControl(previous)
	c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
	storageErr, rollbackErr := errors.New("recovery disk full"), errors.New("firewall rollback failed")
	publications, writes, acknowledgements := 0, 0, 0
	c.applyReload = func(_ context.Context, _, _ settings.Config) error {
		publications++
		if publications == 2 {
			return rollbackErr
		}
		return nil
	}
	c.persistApplied = func(settings.Config) error { writes++; return storageErr }
	c.onApplied = func() { acknowledgements++ }
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	c.onFatal = cancel
	result, _ := c.handle(reloadRequest{ctx: ctx})
	if result.Status != "error" || !errors.Is(context.Cause(ctx), storageErr) || !errors.Is(context.Cause(ctx), rollbackErr) {
		t.Fatal("uncertain runtime was not stopped", result, context.Cause(ctx))
	}
	if writes != 1 || acknowledgements != 0 || !reflect.DeepEqual(c.snapshot(), previous) {
		t.Fatal("failed rollback overwrote or acknowledged the recovery contract", writes, acknowledgements)
	}
}

func TestRecoveredConfigurationApplyRequiresNormalStartup(t *testing.T) {
	for _, edit := range []string{"unchanged", "capture", "membership", "subscription-settings"} {
		t.Run(edit, func(t *testing.T) {
			previous, next := captureInputFixture(), captureInputFixture()
			previous.Subscriptions = []settings.Subscription{{ID: "source", UpdateMode: "manual"}}
			next.Subscriptions = append([]settings.Subscription(nil), previous.Subscriptions...)
			switch edit {
			case "capture":
				next.Capture.DirectIPs = []string{"198.51.100.1/32"}
			case "membership":
				next.Policies[0].SelectionOrder = []string{"country:NL"}
			case "subscription-settings":
				next.Subscriptions[0].UpdateSeconds = 3600
			}
			c := newSubscriptionControl(previous)
			c.recoveryMode = true
			c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
			c.onApplied = func() { t.Fatal("cleared recovery while update workers were suspended") }
			c.applyReload = func(context.Context, settings.Config, settings.Config) error {
				t.Fatal("live-applied to a resolved recovery graph")
				return nil
			}
			c.persistApplied = func(settings.Config) error { t.Fatal("overwrote last working recovery graph"); return nil }
			if result, handled := c.handle(reloadRequest{ctx: t.Context()}); !handled || result.Status != "restart" {
				t.Fatal("recovered process did not request normal startup", result, handled)
			}
			if !reflect.DeepEqual(c.snapshot(), previous) {
				t.Fatal("modified recovered configuration")
			}
		})
	}
}

func TestPublishedRecoverySurvivesDirectorySyncFailureWithoutRuntimeRollback(t *testing.T) {
	paths := Paths{DataRoot: t.TempDir(), RunRoot: t.TempDir()}
	previous, next := captureInputFixture(), captureInputFixture()
	next.Capture.DirectIPs = []string{"198.51.100.1/32"}
	if err := saveAppliedNetwork(paths, previous); err != nil {
		t.Fatal(err)
	}
	c := newSubscriptionControl(previous)
	c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
	publications, syncs := 0, 0
	c.applyReload = func(context.Context, settings.Config, settings.Config) error { publications++; return nil }
	c.persistApplied = func(config settings.Config) error {
		config.SubscriptionURL = ""
		config.Subscriptions = append([]settings.Subscription(nil), config.Subscriptions...)
		for i := range config.Subscriptions {
			config.Subscriptions[i].URL = ""
			config.Subscriptions[i].UpdateMode = "manual"
		}
		encoded, err := EncodeCaptureRestore(config)
		if err != nil {
			return err
		}
		input, err := DecodeCaptureRestore(bytes.NewReader(encoded))
		if err != nil {
			return err
		}
		return writeNetworkRecoverySynced(paths, networkRecovery{Version: 1, Capture: input, Applied: &config, Resolved: &config.Base}, func(string) error {
			syncs++
			visible, err := readAppliedNetwork(paths)
			if err != nil || !reflect.DeepEqual(visible.Capture, next.Capture) {
				t.Fatal("sync hook ran before publication", err)
			}
			return os.ErrPermission
		})
	}
	c.onFatal = func(error) { t.Fatal("directory sync after publication stopped a consistent runtime") }
	if result, _ := c.handle(reloadRequest{ctx: t.Context()}); result.Status != "applied" {
		t.Fatal(result)
	}
	applied, err := readAppliedNetwork(paths)
	if err != nil || syncs != 1 || publications != 1 || !reflect.DeepEqual(applied.Capture, next.Capture) || !reflect.DeepEqual(c.snapshot(), next) {
		t.Fatal("post-rename error rolled back or mixed the committed generation", err, syncs, publications)
	}
}

func TestLivePublicationAndCaptureRollbackFailureStopsUncertainRuntime(t *testing.T) {
	previous, next := captureInputFixture(), captureInputFixture()
	next.Capture.DirectIPs = []string{"198.51.100.1/32"}
	c := newSubscriptionControl(previous)
	c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
	publishErr, rollbackErr := errors.New("pool publication failed"), errors.New("capture rollback failed")
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	c.onFatal = cancel
	c.persistApplied = func(settings.Config) error { t.Fatal("uncommitted settings persisted"); return nil }
	c.applyReload = func(ctx context.Context, previous, next settings.Config) error {
		calls := 0
		return commitLiveCapture(ctx, previous, next, func(context.Context, settings.Config) error {
			calls++
			if calls == 2 {
				return rollbackErr
			}
			return nil
		}, func() error { return publishErr })
	}
	if result, _ := c.handle(reloadRequest{ctx: ctx}); result.Status != "error" || !errors.Is(context.Cause(ctx), publishErr) || !errors.Is(context.Cause(ctx), rollbackErr) {
		t.Fatal(result, context.Cause(ctx))
	}
	if !reflect.DeepEqual(c.snapshot(), previous) {
		t.Fatal("uncertain runtime was acknowledged")
	}
}
