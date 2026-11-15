package daemon

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ati054/xroute-openwrt/internal/settings"
)

func TestConcurrentApplyCannotCommitAnOlderReadAfterANewerRequest(t *testing.T) {
	previous, older, latest := liveFixture(), liveFixture(), liveFixture()
	older.Policies[0].Health.ActiveLivenessSeconds = 4
	latest.Policies[0].Health.ActiveLivenessSeconds = 5
	c := newSubscriptionControl(previous)
	entered, resume, secondRead := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	c.readConfig = func(context.Context) (settings.Config, error) {
		if reads.Add(1) == 1 {
			close(entered)
			<-resume
			return older, nil
		}
		close(secondRead)
		return latest, nil
	}
	c.applyReload = func(context.Context, settings.Config, settings.Config) error { return nil }
	first, second := make(chan reloadResult, 1), make(chan reloadResult, 1)
	go func() { result, _ := c.handle(reloadRequest{ctx: t.Context()}); first <- result }()
	<-entered
	go func() { result, _ := c.handle(reloadRequest{ctx: t.Context()}); second <- result }()
	outOfOrder := false
	select {
	case <-secondRead:
		outOfOrder = true
	case <-time.After(50 * time.Millisecond):
	}
	close(resume)
	a, b := <-first, <-second
	if outOfOrder {
		t.Fatal("a newer request read UCI before the older Apply completed")
	}
	if a.Status != "applied" || b.Status != "applied" || !reflect.DeepEqual(c.snapshot(), latest) {
		t.Fatalf("last acknowledged config lost: %+v %+v", a, b)
	}
}
