package daemon

import (
	"context"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func TestSubscriptionURLReloadDoesNotRequestCoreRestart(t *testing.T) {
	for _, change := range []struct {
		name string
		edit func(*settings.Subscription)
	}{
		{"url", func(s *settings.Subscription) { s.URL = "https://new.example.test/feed" }},
		{"fetch-path", func(s *settings.Subscription) { s.FetchVia = "direct-only" }},
		{"interval", func(s *settings.Subscription) { s.UpdateSeconds = 21600 }},
		{"daily", func(s *settings.Subscription) { s.UpdateMode, s.UpdateTime = "daily", "03:00" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			previous, next := reloadFixture(), reloadFixture()
			source := settings.Subscription{ID: "first", URL: "https://old.example.test/feed", FetchVia: "proxy-first", UpdateMode: "automatic", UpdateSeconds: 1800}
			previous.Subscriptions = []settings.Subscription{source}
			next.Subscriptions = []settings.Subscription{source}
			change.edit(&next.Subscriptions[0])
			control := newSubscriptionControl(previous)
			control.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
			control.applyReload = func(context.Context, settings.Config, settings.Config) error {
				t.Fatal("source settings must not rewrite the live routing pool")
				return nil
			}
			result, handled := control.handle(reloadRequest{ctx: t.Context()})
			if !handled || result.Status != "applied" {
				t.Fatalf("source edit would interrupt the core: %+v handled=%v", result, handled)
			}
			if got := control.snapshot().Subscriptions[0]; got != next.Subscriptions[0] {
				t.Fatal("updated source settings were not accepted")
			}
			if control.sourceCurrent(source) || !control.sourceCurrent(next.Subscriptions[0]) {
				t.Fatal("superseded download settings were still accepted as current")
			}
			oldSignature, _ := refreshSignature(previous.Subscriptions, "first")
			if result := refreshAcceptance(control.snapshot().Subscriptions, "first", oldSignature); result.Status != "pending" {
				t.Fatal("old refresh signature was accepted after a source edit")
			}
		})
	}
}

func TestSubscriptionEditsDoNotHideStructuralRoutingChanges(t *testing.T) {
	previous, next := reloadFixture(), reloadFixture()
	previous.Subscriptions = []settings.Subscription{{ID: "first", URL: "https://old.example.test/feed"}}
	next.Subscriptions = []settings.Subscription{{ID: "first", URL: "https://new.example.test/feed"}}
	next.DNSPort = previous.DNSPort + 1
	control := newSubscriptionControl(previous)
	control.readConfig = func(context.Context) (settings.Config, error) { return next, nil }
	result, handled := control.handle(reloadRequest{ctx: t.Context()})
	if !handled || result.Status != "restart" {
		t.Fatal("structural DNS edit incorrectly bypassed normal apply")
	}
}
