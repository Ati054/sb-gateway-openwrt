package outbound

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/transport/internet"
)

func TestRemoveHandlerRetiresTransportAfterLiveStream(t *testing.T) {
	manager, err := New(context.Background(), &proxyman.OutboundConfig{})
	if err != nil {
		t.Fatal(err)
	}
	settings := &internet.MemoryStreamConfig{}
	handler := &Handler{tag: "generation-a", streamSettings: settings}
	if err := manager.AddHandler(context.Background(), handler); err != nil {
		t.Fatal(err)
	}
	release := settings.AcquireDialer()
	closed := false
	settings.RegisterDialerCleanup("cache", func() {
		closed = true
		// Cleanup must not run with the outbound manager lock held.
		_ = manager.ListHandlers(context.Background())
	})
	if err := manager.RemoveHandler(context.Background(), handler.Tag()); err != nil {
		t.Fatal(err)
	}
	if manager.GetHandler(handler.Tag()) != nil || closed {
		t.Fatal("removal did not detach safely")
	}
	release()
	if !closed {
		t.Fatal("removed outbound's cache was not reclaimed")
	}
}
