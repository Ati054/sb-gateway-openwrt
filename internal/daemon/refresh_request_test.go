package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func TestRefreshAcceptanceRequiresCommittedSource(t *testing.T) {
	source := settings.Subscription{ID: "vpnd_ws", URL: "https://example.test/feed", UpdateMode: "manual", FetchVia: "direct-only"}
	signature, err := refreshSignature([]settings.Subscription{source}, source.ID)
	if err != nil || refreshAcceptance([]settings.Subscription{source}, source.ID, signature).Status != "applied" {
		t.Fatal("committed source rejected")
	}
	old := source
	old.URL = "https://old.test/feed"
	if refreshAcceptance([]settings.Subscription{old}, source.ID, signature).Status != "pending" {
		t.Fatal("request accepted by stale daemon")
	}
	if refreshAcceptance(nil, source.ID, signature).Status != "pending" {
		t.Fatal("new source accepted before reload")
	}
	if _, err := refreshSignature([]settings.Subscription{source}, "other"); err == nil {
		t.Fatal("unknown source accepted")
	}
	both := []settings.Subscription{source, {ID: "vpnd-ws", URL: "https://other.test/feed"}}
	all, _ := refreshSignature(both, "*")
	if refreshAcceptance([]settings.Subscription{source}, "*", all).Status != "pending" {
		t.Fatal("all-source request accepted against partial configuration")
	}
}

func TestRefreshWaitsForStartingDaemonAndMatchingSettings(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Linux Unix socket test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	paths := Paths{RunRoot: t.TempDir()}
	sources := []settings.Subscription{{ID: "vpnd_ws", URL: "https://example.test/feed", UpdateMode: "manual"}}
	completed := make(chan error, 1)
	go func() { completed <- RequestRefresh(ctx, paths, sources, "vpnd_ws") }()
	time.Sleep(100 * time.Millisecond)
	requests, stop, err := startReloadServer(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	first := <-requests
	if first.operation != "refresh" || first.source != "vpnd_ws" {
		t.Fatal("incorrect refresh message")
	}
	first.reply <- reloadResult{Status: "pending"}
	next := <-requests
	next.reply <- refreshAcceptance(sources, next.source, next.signature)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}

func TestRefreshCancellationDoesNotLeaveReplayFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	paths := Paths{RunRoot: t.TempDir()}
	sources := []settings.Subscription{{ID: "vpnd ws", URL: "https://example.test"}}
	if RequestRefresh(ctx, paths, sources, "vpnd ws") == nil {
		t.Fatal("cancelled request accepted")
	}
	if entries, err := os.ReadDir(paths.RunRoot); err != nil || len(entries) != 0 {
		t.Fatal("request left replay state")
	}
}

func TestRefreshAcknowledgesWhileSubscriptionWorkerIsBusy(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Linux Unix socket test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sources := []settings.Subscription{{ID: "vpnd ws", URL: "https://example.test", UpdateMode: "automatic"}}
	control := newSubscriptionControl(settings.Config{Subscriptions: sources})
	paths := Paths{RunRoot: t.TempDir()}
	_, stop, err := startReloadServer(ctx, paths, control.handle)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// No worker consumes either channel until the command has already returned.
	if err := RequestRefresh(ctx, paths, sources, "vpnd ws"); err != nil {
		t.Fatal(err)
	}
	if queued := <-control.refresh; queued != "vpnd ws" {
		t.Fatal(queued)
	}
	for i := 0; i < cap(control.refresh); i++ {
		control.refresh <- "vpnd ws"
	}
	signature, _ := refreshSignature(sources, "vpnd ws")
	if result, _ := control.handle(reloadRequest{operation: "refresh", source: "vpnd ws", signature: signature, ctx: ctx}); result.Status != "error" {
		t.Fatal("queue is not bounded")
	}
	stale := sources[0]
	stale.URL = "https://old.test"
	control.update(settings.Config{Subscriptions: []settings.Subscription{stale}})
	if result, _ := control.handle(reloadRequest{operation: "refresh", source: "vpnd ws", signature: signature, ctx: ctx}); result.Status != "pending" {
		t.Fatal("stale config accepted")
	}
}

func TestBusyControlClassifiesStructuralChangesWithoutWaiting(t *testing.T) {
	current := reloadFixture()
	if result, handled := classifyControlReload(current, current); !handled || result.Status != "applied" {
		t.Fatal(result)
	}
	candidate := current
	candidate.Subscriptions = []settings.Subscription{{ID: "new", URL: "https://example.test"}}
	if result, handled := classifyControlReload(current, candidate); !handled || result.Status != "restart" {
		t.Fatal(result)
	}
	candidate = reloadFixture()
	candidate.Policies[0].Health.ActiveLivenessSeconds++
	if _, handled := classifyControlReload(current, candidate); handled {
		t.Fatal("runtime mutation escaped serial loop")
	}
}
