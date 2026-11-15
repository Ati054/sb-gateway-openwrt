package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Ati054/xroute-openwrt/internal/agentport"
	"github.com/Ati054/xroute-openwrt/internal/settings"
)

func liveFixture() settings.Config {
	c := reloadFixture()
	c.Subscriptions = []settings.Subscription{{ID: "first", URL: "https://old.example.test/feed", UpdateMode: "automatic", UpdateSeconds: 3600}}
	return c
}

func TestLiveApplyCombinationsPublishPoolCaptureAndSourceTogether(t *testing.T) {
	for mask := 1; mask < 16; mask++ {
		previous, next := liveFixture(), liveFixture()
		label := ""
		if mask&1 != 0 {
			next.Policies[0].Health.ActiveLivenessSeconds = 5
			label += "health+"
		}
		if mask&2 != 0 {
			next.Policies[0].SelectionOrder = []string{"country:CA", "country:NL"}
			label += "members+"
		}
		if mask&4 != 0 {
			next.Subscriptions[0].URL = "https://new.example.test/feed"
			label += "source+"
		}
		if mask&8 != 0 {
			next.Capture.DirectIPs = []string{"203.0.113.1/32"}
			label += "capture+"
		}
		t.Run(label, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pool.json")
			nodes := membershipNodes()
			if err := agentport.WritePool(path, nodes, previous.Policies); err != nil {
				t.Fatal(err)
			}
			c := newSubscriptionControl(previous)
			c.poolNodes = nodes
			c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
			captureCalls := 0
			c.applyReload = func(ctx context.Context, old, next settings.Config) error {
				return c.applyLiveReloadLocked(ctx, path, old, next, func(ctx context.Context, config settings.Config) error {
					captureCalls++
					if !reflect.DeepEqual(config, next) {
						t.Fatal("capture got a mixed config generation")
					}
					return nil
				})
			}
			if result, handled := c.handle(reloadRequest{ctx: t.Context()}); !handled || result.Status != "applied" {
				t.Fatalf("hot edit interrupted runtime: %+v", result)
			}
			if !reflect.DeepEqual(c.snapshot(), next) {
				t.Fatal("incomplete acknowledged config")
			}
			want := 1
			if mask&2 != 0 {
				want = 2
			}
			if got := poolMembers(t, path); len(got) != want {
				t.Fatal(got)
			}
			wantCapture := 0
			if mask&8 != 0 {
				wantCapture = 1
			}
			if captureCalls != wantCapture {
				t.Fatalf("capture operations=%d want=%d", captureCalls, wantCapture)
			}
			if mask&4 != 0 && c.sourceCurrent(previous.Subscriptions[0]) {
				t.Fatal("stale download still accepted")
			}
		})
	}
}

// Every configuration field has an explicit reviewed impact. New settings must
// be classified here; they cannot silently inherit an untested Apply behavior.
func TestEveryConfigFieldHasReviewedApplyImpact(t *testing.T) {
	live := map[string]bool{"Capture": true, "SubscriptionURL": true, "UpdateMode": true, "UpdateSeconds": true, "MaxNodes": true, "Subscriptions": true, "Policies": true}
	restart := map[string]bool{"URLTestPlanningMode": true, "Enabled": true, "RouteEnabled": true, "LANInterface": true, "ProviderID": true, "RulesetUpdateMode": true, "RulesetUpdateSeconds": true, "DNSMode": true, "DNSHandoffConfirmed": true, "DNSProxyURL": true, "DNSDirectURL": true, "DNSPort": true, "RuleGroups": true, "Base": true}
	typeOf := reflect.TypeFor[settings.Config]()
	if len(live)+len(restart) != typeOf.NumField() {
		t.Fatal("Config fields changed: review the Apply impact table")
	}
	for i := 0; i < typeOf.NumField(); i++ {
		name := typeOf.Field(i).Name
		if !live[name] && !restart[name] {
			t.Fatalf("unreviewed field %s", name)
		}
		t.Run(name, func(t *testing.T) {
			previous, next := liveFixture(), liveFixture()
			field := reflect.ValueOf(&next).Elem().FieldByName(name)
			switch name {
			case "Capture":
				next.Capture.DirectIPs = []string{"203.0.113.1/32"}
			case "Base":
				next.Base.APIPort++
			case "Policies":
				next.Policies[0].Health.ActiveLivenessSeconds = 5
			case "Subscriptions":
				next.Subscriptions[0].URL = "https://new.example.test/feed"
			case "RuleGroups":
				field.Set(reflect.MakeSlice(field.Type(), 1, 1))
			default:
				switch field.Kind() {
				case reflect.String:
					field.SetString(field.String() + "-changed")
				case reflect.Bool:
					field.SetBool(!field.Bool())
				case reflect.Int:
					field.SetInt(field.Int() + 1)
				default:
					t.Fatal("provide a field-specific impact fixture")
				}
			}
			if _, allowed := liveReloadBase(previous, next); allowed != live[name] {
				t.Fatalf("field %s live=%v want=%v", name, allowed, live[name])
			}
			// A safe edit alongside a structural one must not conceal it.
			next.Policies[0].SelectionOrder = []string{"country:NL"}
			if _, allowed := liveReloadBase(previous, next); allowed != live[name] {
				t.Fatalf("mixed field %s live=%v", name, allowed)
			}
		})
	}
}

func TestSourceIdentityChangesRemainStructuralWithOtherLiveEdits(t *testing.T) {
	for _, edit := range []func(*settings.Config){
		func(c *settings.Config) { c.Subscriptions[0].ID = "renamed" },
		func(c *settings.Config) {
			c.Subscriptions = append(c.Subscriptions, settings.Subscription{ID: "second"})
		},
		func(c *settings.Config) { c.Subscriptions = nil },
	} {
		previous, next := liveFixture(), liveFixture()
		edit(&next)
		next.Policies[0].SelectionOrder = []string{"country:NL"}
		next.Capture.DirectIPs = []string{"203.0.113.1/32"}
		if result, handled := classifyControlReload(previous, next); !handled || result.Status != "restart" {
			t.Fatal(result)
		}
	}
}

func TestCombinedCaptureRollbackSurvivesCancellationAndReportsBothErrors(t *testing.T) {
	primary, restore := errors.New("pool publication failed"), errors.New("capture restoration failed")
	previous, next := liveFixture(), liveFixture()
	next.Capture.DirectIPs = []string{"203.0.113.1/32"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	err := commitLiveCapture(ctx, previous, next, func(attempt context.Context, config settings.Config) error {
		calls++
		if attempt.Err() != nil {
			t.Fatal("cancelled context used for restoration")
		}
		if calls == 1 {
			if !reflect.DeepEqual(config, next) {
				t.Fatal("wrong target")
			}
			cancel()
			return nil
		}
		if !reflect.DeepEqual(config, previous) {
			t.Fatal("wrong rollback")
		}
		return restore
	}, func() error { return primary })
	if calls != 2 || !errors.Is(err, primary) || !errors.Is(err, restore) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestFailedCaptureDoesNotPublishPool(t *testing.T) {
	failed := errors.New("nft rejected transaction")
	err := commitLiveCapture(t.Context(), liveFixture(), liveFixture(), func(context.Context, settings.Config) error { return failed }, func() error { t.Fatal("pool published after capture failure"); return nil })
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
}

func TestInvalidMixedMembershipDoesNotChangeCapture(t *testing.T) {
	previous, next := liveFixture(), liveFixture()
	next.Policies[0].SelectionOrder = []string{"invalid"}
	next.Capture.DirectIPs = []string{"203.0.113.1/32"}
	c := newSubscriptionControl(previous)
	c.poolNodes = membershipNodes()
	c.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
	c.applyReload = func(ctx context.Context, old, next settings.Config) error {
		return c.applyLiveReloadLocked(ctx, filepath.Join(t.TempDir(), "pool.json"), old, next, func(context.Context, settings.Config) error {
			t.Fatal("capture changed before pool validation")
			return nil
		})
	}
	if result, _ := c.handle(reloadRequest{ctx: t.Context()}); result.Status != "error" {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(c.snapshot(), previous) {
		t.Fatal("failed mixed config acknowledged")
	}
}
