package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/dnsproxy"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func TestWANDNSEventUsesExistingControlSocketAndCoalesces(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	control := newSubscriptionControl(settings.Config{DNSDirectURL: dnsproxy.WANResolverURL, DNSMode: "proxy-only"})
	control.readConfig = func(context.Context) (settings.Config, error) {
		t.Error("interface event reread UCI or requested restart")
		return settings.Config{}, nil
	}
	paths := Paths{RunRoot: t.TempDir()}
	_, stop, err := startReloadServer(ctx, paths, control.handle)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	requestCtx, requestCancel := context.WithTimeout(ctx, time.Second)
	defer requestCancel()
	if err := RequestWANDNSRefresh(requestCtx, paths); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		result, handled := control.handle(reloadRequest{operation: "wan-dns-refresh", ctx: ctx})
		if !handled || result.Status != "applied" {
			t.Fatal("event rejected")
		}
	}
	if len(control.wanChanges) != 1 {
		t.Fatal("interface burst did not coalesce")
	}
	<-control.wanChanges
	other := newSubscriptionControl(settings.Config{DNSDirectURL: "https://resolver.example/dns-query", DNSMode: "proxy-only"})
	result, handled := other.handle(reloadRequest{operation: "wan-dns-refresh", ctx: ctx})
	if !handled || result.Status != "applied" || len(other.wanChanges) != 0 {
		t.Fatal("ISP event changed an explicitly selected DoH resolver")
	}
	control.mu.Lock()
	ack := make(chan struct{})
	go func() { control.handle(reloadRequest{operation: "wan-dns-refresh", ctx: ctx}); close(ack) }()
	select {
	case <-ack:
		control.mu.Unlock()
	case <-time.After(time.Second):
		control.mu.Unlock()
		t.Fatal("WAN event waited for catalogue/configuration work")
	}
}
