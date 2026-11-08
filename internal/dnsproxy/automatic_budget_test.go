package dnsproxy

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutomaticDNSSlowUDPIsNotCanceledByAHedgedTCP(t *testing.T) {
	for _, delay := range []int64{1100, 3000} {
		t.Run(fmt.Sprint(delay), func(t *testing.T) {
			f := newAutomaticDNSFixture(t)
			f.udpAnswers.Store(true)
			f.udpDelayMS.Store(delay)
			f.tcpAnswers.Store(false)
			var preference dnsTransportPreference
			for i := 0; i < 2; i++ {
				if _, err := exchangeAutomaticDNS(t.Context(), f.endpoint, dnsQuery(fmt.Sprintf("slow-%d.example", i)), &preference); err != nil {
					t.Fatal(err)
				}
			}
			if preference.Load() || !preference.Known() || f.udpCalls.Load() != 2 || f.tcpCalls.Load() != 1 {
				t.Fatalf("slow UDP lost or cold probe repeated: UDP=%d TCP=%d", f.udpCalls.Load(), f.tcpCalls.Load())
			}
		})
	}
}

func TestAutomaticDNSKnownTCPFailureCanRecoverSlowUDP(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.udpAnswers.Store(true)
	f.udpDelayMS.Store(1100)
	f.tcpAnswers.Store(false)
	var preference dnsTransportPreference
	preference.Store(true)
	for i := 0; i < 2; i++ {
		if _, err := exchangeAutomaticDNS(t.Context(), f.endpoint, dnsQuery(fmt.Sprintf("recover-%d.example", i)), &preference); err != nil {
			t.Fatal(err)
		}
	}
	if preference.Load() || f.tcpCalls.Load() != 1 || f.udpCalls.Load() != 2 {
		t.Fatal("slow recovery was starved or failed transport repeatedly probed")
	}
}

func TestAutomaticDNSDeniedHedgeRotatesAfterFailure(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.udpAnswers.Store(true)
	f.tcpAnswers.Store(false)
	var preference dnsTransportPreference
	preference.Store(true)
	var denied atomic.Int32
	ctx := context.WithValue(t.Context(), dnsTransportBudgetKey{}, dnsTransportBorrow(func() (func(), bool) { denied.Add(1); return nil, false }))
	short, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	if _, err := exchangeAutomaticDNS(short, f.endpoint, dnsQuery("no-room.example"), &preference); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded first timeout: %v", err)
	}
	if denied.Load() != 1 || f.udpCalls.Load() != 0 || preference.Known() {
		t.Fatal("denied hedge expanded the budget or retained a dead preference")
	}
	if _, err := exchangeAutomaticDNS(ctx, f.endpoint, dnsQuery("next.example"), &preference); err != nil {
		t.Fatal(err)
	}
	if preference.Load() || f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 1 {
		t.Fatal("next query did not recover without extra capacity")
	}
}

func TestAutomaticDNSStaleTimeoutCannotEraseConcurrentSameTransportSuccess(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.tcpAnswers.Store(false)
	var preference dnsTransportPreference
	preference.Store(true)
	short, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := exchangeAutomaticDNS(short, f.endpoint, dnsQuery("old.example"), &preference)
		done <- err
	}()
	until := time.Now().Add(time.Second)
	for f.tcpBlocked.Load() == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if f.tcpBlocked.Load() != 1 {
		t.Fatal("old request did not reach TCP")
	}
	f.tcpAnswers.Store(true)
	if _, err := exchangeAutomaticDNS(t.Context(), f.endpoint, dnsQuery("new.example"), &preference); err != nil {
		t.Fatal(err)
	}
	observed := preference.state.Load()
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if !preference.Known() || !preference.Load() || preference.state.Load() != observed {
		t.Fatal("stale timeout erased newer successful TCP")
	}
	preference.remember(true, preference.latency())
	if preference.state.Load() == observed {
		t.Fatal("identical success did not advance observation version")
	}
}

func TestAutomaticDNSHedgeSharesAdmissionAndReleasesBeforeCompletion(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.tcpAnswers.Store(false)
	s := &server{requestBudget: 4 * dnsRequestMemory, config: Config{DefaultPolicy: "main"}}
	r := s.requests()
	defer func() { r.close(); s.workers.Wait() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 2)
	query := dnsQuery("bounded.example")
	work := func(ctx context.Context) ([]byte, error) { return exchangeAutomaticDNS(ctx, f.endpoint, query, nil) }
	for i := 0; i < 2; i++ {
		if err := r.submit(ctx, "wan", "same", query, work, func(_ []byte, err error) { done <- err }); err != nil {
			t.Fatal(err)
		}
	}
	stats := waitDNSStats(t, s, func(m map[string]uint64) bool { return m["active_upstream"] == 2 && m["waiting_shared"] == 1 })
	if stats["dns_work_bytes"] > uint64(s.requestBudget) {
		t.Fatal("hedge exceeded budget")
	}
	if release, ok := r.borrowTransport(ctx, "wan"); ok {
		release()
		t.Fatal("third operation admitted beyond existing work pool")
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation retained a socket or lease")
		}
	}
	waitDNSStats(t, s, func(m map[string]uint64) bool {
		return m["active_upstream"] == 0 && m["dns_work_bytes"] == 0 && m["waiting_shared"] == 0
	})
}

func TestAutomaticDNSNonQueriesDoNotHedgeOnTheWire(t *testing.T) {
	for _, kind := range []uint16{251, 252, 0x8004, 0x8005} {
		f := newAutomaticDNSFixture(t)
		q := dnsQuery("unsafe.example")
		if kind < 0x8000 {
			q[len(q)-4] = byte(kind >> 8)
			q[len(q)-3] = byte(kind)
		} else {
			q[2] = byte(kind&15) << 3
		}
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
		_, err := exchangeAutomaticDNS(ctx, f.endpoint, q, nil)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 0 {
			t.Fatalf("unsafe query replayed: kind=%d UDP=%d TCP=%d err=%v", kind, f.udpCalls.Load(), f.tcpCalls.Load(), err)
		}
	}
}
