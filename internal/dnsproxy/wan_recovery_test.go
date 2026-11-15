package dnsproxy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWANOneEventRecoversAfterStatusRetryBatchExhaustion(t *testing.T) {
	var calls atomic.Int32
	state := &wanDNSState{lookup: func(context.Context) ([]Endpoint, error) {
		n := calls.Add(1)
		if n >= 2 && n <= 6 {
			return nil, errors.New("temporary status reader failure")
		}
		address := "192.0.2.53:53"
		if n >= 7 {
			address = "192.0.2.54:53"
		}
		return []Endpoint{{Address: address, Interface: "eth1"}}, nil
	}}
	if err := state.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	changes := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { defer close(done); state.watch(ctx, changes) }()
	changes <- struct{}{}
	deadline := time.Now().Add(8 * time.Second)
	observedUnavailable := false
	for time.Now().Before(deadline) {
		snapshot := state.current.Load()
		if snapshot != nil && len(snapshot.servers) == 0 {
			observedUnavailable = true
		}
		if snapshot != nil && len(snapshot.servers) == 1 && snapshot.servers[0].Address == "192.0.2.54:53" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	snapshot := state.current.Load()
	if snapshot == nil || len(snapshot.servers) != 1 || snapshot.servers[0].Address != "192.0.2.54:53" {
		t.Fatalf("one failed notification left ISP DNS unavailable after status recovery: calls=%d", calls.Load())
	}
	if !observedUnavailable {
		t.Fatal("exhausted status verification kept authorizing the stale WAN snapshot")
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 7 {
		t.Fatalf("stable WAN continued to be polled: %d", calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery watcher did not stop")
	}
}

func TestWANRecoveryWaitStopsOnCancellationOrClosedNotifications(t *testing.T) {
	for _, closed := range []bool{false, true} {
		state := &wanDNSState{lookup: func(context.Context) ([]Endpoint, error) { return nil, errors.New("status reader unavailable") }}
		ctx, cancel := context.WithCancel(t.Context())
		changes := make(chan struct{}, 1)
		done := make(chan struct{})
		go func() { defer close(done); state.watch(ctx, changes) }()
		changes <- struct{}{}
		time.Sleep(80 * time.Millisecond)
		if closed {
			close(changes)
		} else {
			cancel()
		}
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Fatal("closed notification/cancellation retained recovery work")
		}
		cancel()
	}
}

func TestWANConfirmedMissingStatusEndsRecoveryWithoutPolling(t *testing.T) {
	var calls atomic.Int32
	state := &wanDNSState{lookup: func(context.Context) ([]Endpoint, error) {
		if calls.Add(1) == 1 {
			return []Endpoint{{Address: "192.0.2.53:53", Interface: "eth1"}}, nil
		}
		return nil, errWANDNSUnavailable
	}}
	if err := state.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	changes := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { defer close(done); state.watch(ctx, changes) }()
	changes <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for len(state.current.Load().servers) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(state.current.Load().servers) != 0 {
		t.Fatal("confirmed down kept using the old resolver")
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatal("confirmed missing WAN started periodic discovery")
	}
	cancel()
	<-done
}
