package dnsproxy

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type automaticDNSFixture struct {
	endpoint               Endpoint
	udpAnswers, tcpAnswers atomic.Bool
	udpCalls, tcpCalls     atomic.Int32
	rcode                  atomic.Uint32
	udpTruncated           atomic.Bool
	udpDelayMS             atomic.Int64
	tcpDelayMS             atomic.Int64
	tcpBlocked             atomic.Int32
}

func newAutomaticDNSFixture(t *testing.T) *automaticDNSFixture {
	t.Helper()
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp4", tcp.Addr().String())
	if err != nil {
		tcp.Close()
		t.Fatal(err)
	}
	f := &automaticDNSFixture{endpoint: Endpoint{Protocol: "dns", Address: tcp.Addr().String()}}
	f.tcpAnswers.Store(true)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		var message [4096]byte
		for {
			n, peer, err := udp.ReadFrom(message[:])
			if err != nil {
				return
			}
			f.udpCalls.Add(1)
			if f.udpAnswers.Load() {
				time.Sleep(time.Duration(f.udpDelayMS.Load()) * time.Millisecond)
				answer := cacheableDoHReply(message[:n], 42)
				answer[3] = answer[3]&0xf0 | byte(f.rcode.Load())
				if f.udpTruncated.Load() {
					answer[2] |= 2
				}
				_, _ = udp.WriteTo(answer, peer)
			}
		}
	}()
	go func() {
		defer workers.Done()
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				var size [2]byte
				if _, err := io.ReadFull(conn, size[:]); err != nil {
					return
				}
				query := make([]byte, binary.BigEndian.Uint16(size[:]))
				if _, err := io.ReadFull(conn, query); err != nil {
					return
				}
				f.tcpCalls.Add(1)
				if !f.tcpAnswers.Load() {
					f.tcpBlocked.Add(1)
					io.Copy(io.Discard, conn)
					return
				}
				answer := cacheableDoHReply(query, 42)
				answer[3] = answer[3]&0xf0 | byte(f.rcode.Load())
				time.Sleep(time.Duration(f.tcpDelayMS.Load()) * time.Millisecond)
				binary.BigEndian.PutUint16(size[:], uint16(len(answer)))
				_, _ = conn.Write(append(size[:], answer...))
			}()
		}
	}()
	t.Cleanup(func() { tcp.Close(); udp.Close(); workers.Wait() })
	return f
}

func TestAutomaticDNSTruncationGivesTCPTheRemainingSharedDeadline(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.udpAnswers.Store(true)
	f.udpTruncated.Store(true)
	f.tcpDelayMS.Store(1100)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var preference dnsTransportPreference
	if _, err := exchangeAutomaticDNS(ctx, f.endpoint, dnsQuery("large.example"), &preference); err != nil {
		t.Fatalf("TCP after TC=1 lost the remaining total budget: %v", err)
	}
	if !preference.Load() || f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 1 {
		t.Fatalf("actual TCP transport was not retained: UDP=%d TCP=%d", f.udpCalls.Load(), f.tcpCalls.Load())
	}
	runtimeCtx, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	if _, err := exchangeAutomaticDNS(runtimeCtx, f.endpoint, dnsQuery("runtime-large.example"), &preference); err != nil {
		t.Fatal(err)
	}
	if f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 2 {
		t.Fatalf("known slow TCP was needlessly probed again: UDP=%d TCP=%d", f.udpCalls.Load(), f.tcpCalls.Load())
	}
}

func TestAutomaticDNSKnownUDPHasTimeForAnOrdinarySlowAnswer(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.udpAnswers.Store(true)
	f.udpDelayMS.Store(1100)
	var preference dnsTransportPreference
	preference.remember(false, 1100*time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := exchangeAutomaticDNS(ctx, f.endpoint, dnsQuery("slow-udp.example"), &preference); err != nil {
		t.Fatal(err)
	}
	if preference.Load() || f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 0 {
		t.Fatal("known UDP was replaced before its ordinary answer arrived")
	}
}

func TestAutomaticDNSTruncatedUDPDoesNotLoopAfterTCPTimeout(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.udpAnswers.Store(true)
	f.udpTruncated.Store(true)
	f.tcpAnswers.Store(false)
	ctx, cancel := context.WithTimeout(t.Context(), 160*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := exchangeAutomaticDNS(ctx, f.endpoint, dnsQuery("truncated-timeout.example"), nil)
	var wire *upstreamExchangeError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &wire) || wire.protocol != "tcp" || f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 1 {
		t.Fatalf("truncation failure lost TCP identity or repeated UDP: %v, UDP=%d TCP=%d", err, f.udpCalls.Load(), f.tcpCalls.Load())
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond || elapsed > time.Second {
		t.Fatalf("TCP did not use the remaining common deadline: %v", elapsed)
	}
}

func TestAutomaticDNSCancellationDuringTruncatedTCPDoesNotRetryUDP(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.udpAnswers.Store(true)
	f.udpTruncated.Store(true)
	f.tcpAnswers.Store(false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := exchangeAutomaticDNS(ctx, f.endpoint, dnsQuery("cancel-tc.example"), nil)
		done <- err
	}()
	until := time.Now().Add(time.Second)
	for (f.tcpCalls.Load() == 0 || f.udpCalls.Load() == 0) && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if f.tcpCalls.Load() != 1 {
		t.Fatal("TC did not start TCP")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 1 {
			t.Fatalf("canceled TC exchange was retried: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TCP cancellation did not stop the exchange")
	}
}

func TestAutomaticDNSDoesNotRetryValidNegativeAnswers(t *testing.T) {
	for _, rcode := range []uint32{2, 3, 5} {
		f := newAutomaticDNSFixture(t)
		f.udpAnswers.Store(true)
		f.rcode.Store(rcode)
		var preference dnsTransportPreference
		preference.Store(false)
		answer, err := exchangeAutomaticDNS(t.Context(), f.endpoint, dnsQuery("negative.example"), &preference)
		if err != nil || len(answer) < 12 || uint32(answer[3]&15) != rcode {
			t.Fatalf("negative answer changed: code=%d err=%v", rcode, err)
		}
		if f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 0 {
			t.Fatal("valid DNS failure triggered a transport retry")
		}
	}
}

func TestAutomaticDNSCancellationDoesNotStartAnotherTransport(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	var preference dnsTransportPreference
	preference.Store(false)
	go func() {
		_, err := exchangeAutomaticDNS(ctx, f.endpoint, dnsQuery("cancel.example"), &preference)
		done <- err
	}()
	until := time.Now().Add(time.Second)
	for f.udpCalls.Load() == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if f.udpCalls.Load() != 1 {
		t.Fatal("first UDP attempt did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || f.tcpCalls.Load() != 0 {
			t.Fatalf("canceled query started fallback: %v, TCP=%d", err, f.tcpCalls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("canceled query did not stop")
	}
}

func TestAutomaticDNSDoesNotReplayTransfersOrNonQueries(t *testing.T) {
	for _, kind := range []uint16{251, 252} {
		query := dnsQuery("zone.example")
		binary.BigEndian.PutUint16(query[len(query)-4:], kind)
		if repeatableDNSQuery(query) {
			t.Fatal("zone transfer was considered an ordinary lookup")
		}
	}
	for _, opcode := range []byte{4, 5} {
		query := dnsQuery("update.example")
		query[2] = opcode << 3
		if repeatableDNSQuery(query) {
			t.Fatal("NOTIFY/UPDATE was considered repeatable")
		}
	}
}

func TestWANTransportPreferenceIsInvalidatedByUplinkIdentity(t *testing.T) {
	endpoint := Endpoint{Address: "192.0.2.53:53", Interface: "eth1", WANIdentity: "source-a/gateway-a"}
	state := &wanDNSState{lookup: func(context.Context) ([]Endpoint, error) { return []Endpoint{endpoint}, nil }}
	for _, changed := range []string{"interface", "source/route"} {
		if err := state.refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		previous := state.current.Load()
		previous.tcpPreferred[0].Store(true)
		if changed == "interface" {
			endpoint.Interface = "eth2"
		} else {
			endpoint.WANIdentity = "source-b/gateway-b"
		}
		if err := state.refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if next := state.current.Load(); next == previous || next.tcpPreferred[0].Load() {
			t.Fatal("transport preference crossed a changed WAN identity")
		}
	}
}

func TestAutomaticWANDNSUsesTCPWhenUDPDoesNotAnswerAndRemembersIt(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.tcpDelayMS.Store(1100)
	snapshot := &wanDNSSnapshot{servers: []Endpoint{f.endpoint}, tcpPreferred: make([]dnsTransportPreference, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := exchangeWANDNS(ctx, snapshot, dnsQuery("first.example")); err != nil {
		t.Fatal(err)
	}
	if !snapshot.tcpPreferred[0].Load() || f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 1 {
		t.Fatalf("TCP fallback was not retained: udp=%d tcp=%d", f.udpCalls.Load(), f.tcpCalls.Load())
	}
	if _, err := exchangeWANDNS(t.Context(), snapshot, dnsQuery("second.example")); err != nil {
		t.Fatal(err)
	}
	if f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 2 {
		t.Fatal("working TCP retried blocked UDP")
	}
}

func TestAutomaticDNSRecoversUDPIfPreferredTCPStopsWorking(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.udpAnswers.Store(true)
	f.tcpAnswers.Store(false)
	var preference dnsTransportPreference
	preference.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := exchangeAutomaticDNS(ctx, f.endpoint, dnsQuery("recover.example"), &preference); err != nil {
		t.Fatal(err)
	}
	if preference.Load() || f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 1 {
		t.Fatal("UDP recovery did not replace failed TCP preference")
	}
}

func TestAutomaticDNSKeepsOneSharedDeadlineAndDoesNotReplayUpdates(t *testing.T) {
	for _, update := range []bool{false, true} {
		t.Run(map[bool]string{false: "query", true: "update"}[update], func(t *testing.T) {
			f := newAutomaticDNSFixture(t)
			f.tcpAnswers.Store(false)
			query := dnsQuery("deadline.example")
			if update {
				query[2] |= 5 << 3
			}
			ctx, cancel := context.WithTimeout(t.Context(), 160*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := exchangeAutomaticDNS(ctx, f.endpoint, query, nil)
			if err == nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatalf("deadline lost: %v, %v", err, time.Since(start))
			}
			if update && (f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 0) {
				t.Fatal("non-QUERY request was replayed")
			}
		})
	}
}

func TestDirectPreflightCarriesWorkingTCPIntoTheDNSListener(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	raw := "dns://" + f.endpoint.Address
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	bootstrap, err := PreflightDirect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(Config{Port: 1053, ProxyPort: 19080, ProxyURL: "https://1.1.1.1/dns-query", DirectURL: raw, DirectBootstrapIP: bootstrap.IP, DirectBootstrap: bootstrap, Mode: "proxy-only", DefaultPolicy: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.server.directTCPPreferred.Load() {
		t.Fatal("listener discarded the successful preflight transport")
	}
	if _, err := exchangeAutomaticDNS(t.Context(), f.endpoint, dnsQuery("runtime.example"), &prepared.server.directTCPPreferred); err != nil {
		t.Fatal(err)
	}
	if f.udpCalls.Load() != 1 || f.tcpCalls.Load() != 2 {
		t.Fatal("listener repeated the failed UDP preflight")
	}
}

func TestDirectPreflightCarriesKnownUDPIntoTheDNSListener(t *testing.T) {
	f := newAutomaticDNSFixture(t)
	f.udpAnswers.Store(true)
	f.udpDelayMS.Store(1100)
	f.tcpAnswers.Store(false)
	raw := "dns://" + f.endpoint.Address
	bootstrap, err := PreflightDirect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(Config{Port: 1053, ProxyPort: 19080, ProxyURL: "https://1.1.1.1/dns-query", DirectURL: raw, DirectBootstrapIP: bootstrap.IP, DirectBootstrap: bootstrap, Mode: "proxy-only", DefaultPolicy: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.server.directTCPPreferred.Known() || prepared.server.directTCPPreferred.Load() {
		t.Fatal("known UDP preflight was lost")
	}
	f.udpDelayMS.Store(1100)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := exchangeAutomaticDNS(ctx, f.endpoint, dnsQuery("known-udp-runtime.example"), &prepared.server.directTCPPreferred); err != nil {
		t.Fatal(err)
	}
	if f.udpCalls.Load() != 2 || f.tcpCalls.Load() != 1 {
		t.Fatal("known UDP was cold-probed again")
	}
}

func TestWANPreflightTransportSurvivesInitialNetifdRefresh(t *testing.T) {
	lookup := func(context.Context) ([]Endpoint, error) {
		return []Endpoint{{Address: "192.0.2.53:53", Interface: "eth1"}}, nil
	}
	state := &wanDNSState{lookup: lookup}
	if err := state.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := state.current.Load()
	first.tcpPreferred[0].Store(true)
	prepared, err := Prepare(Config{Port: 1053, ProxyPort: 19080, ProxyURL: "https://1.1.1.1/dns-query", DirectURL: WANResolverURL, DirectBootstrap: DirectBootstrap{upstream: WANResolverURL, wan: first}, WANNameservers: lookup, Mode: "proxy-only", DefaultPolicy: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.server.wanDNS.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if prepared.server.wanDNS.current.Load() != first || !first.tcpPreferred[0].Load() {
		t.Fatal("unchanged WAN refresh discarded the preflight preference")
	}
}
