package dnsproxy

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const transportStamp uint64 = 1 << 32
const transportDataMask uint64 = transportStamp - 1

// Low bits: unknown/UDP/TCP/unknown-preferring-TCP. Each success advances the
// observation version, including identical protocol/latency observations.
type dnsTransportPreference struct{ state atomic.Uint64 }

func (p *dnsTransportPreference) Load() bool {
	choice := p.state.Load() & 3
	return choice == 2 || choice == 3
}
func (p *dnsTransportPreference) Known() bool {
	choice := p.state.Load() & 3
	return choice == 1 || choice == 2
}
func (p *dnsTransportPreference) Store(tcp bool) { p.remember(tcp, 0) }
func (p *dnsTransportPreference) remember(tcp bool, elapsed time.Duration) {
	choice := uint64(1)
	if tcp {
		choice = 2
	}
	micros := uint64(max(time.Duration(0), min(elapsed, dnsLookupTimeout)) / time.Microsecond)
	for {
		previous := p.state.Load()
		next := ((previous + transportStamp) &^ transportDataMask) | (micros << 2) | choice
		if p.state.CompareAndSwap(previous, next) {
			return
		}
	}
}
func (p *dnsTransportPreference) latency() time.Duration {
	return time.Duration((p.state.Load()&transportDataMask)>>2) * time.Microsecond
}

func repeatableDNSQuery(query []byte) bool {
	if len(query) < 12 || query[2]&0xf8 != 0 || query[4] != 0 || query[5] > 1 {
		return false
	}
	if query[5] == 0 {
		return true
	}
	var parser dnsmessage.Parser
	if _, err := parser.Start(query); err != nil {
		return false
	}
	question, err := parser.Question()
	return err == nil && question.Type != 251 && question.Type != 252
}

type dnsTransportBudgetKey struct{}
type dnsTransportBorrow func() (func(), bool)
type dnsTransportResult struct {
	protocol  string
	body      []byte
	err       error
	truncated bool
	elapsed   time.Duration
}

// A second transport never shortens the first one's deadline. During normal
// service it needs an extra reservation from the existing upstream budget.
// Standalone startup preflight has no listener jobs and uses at most two sockets.
func exchangeAutomaticDNS(ctx context.Context, endpoint Endpoint, query []byte, preference *dnsTransportPreference) (body []byte, resultErr error) {
	shared, stopShared := context.WithTimeout(ctx, dnsLookupTimeout)
	defer stopShared()
	wireCtx, stopWire := context.WithCancel(shared)
	var hint uint64
	if preference != nil {
		hint = preference.state.Load()
	}
	choice := hint & 3
	first := "udp"
	if choice == 2 || choice == 3 {
		first = "tcp"
	}
	other := "tcp"
	if first == "tcp" {
		other = "udp"
	}
	repeatable := repeatableDNSQuery(query)
	results := make(chan dnsTransportResult, 2)
	var workers sync.WaitGroup
	var releases []func()
	defer func() {
		parentErr := shared.Err()
		if resultErr != nil && repeatable && preference != nil && !errors.Is(parentErr, context.Canceled) {
			// A late failed query cannot erase a newer successful observation.
			next := uint64(0)
			if first == "udp" {
				next = 3
			}
			preference.state.CompareAndSwap(hint, ((hint+transportStamp)&^transportDataMask)|next)
		}
		stopWire()
		workers.Wait()
		for len(results) > 0 {
			<-results
		}
		for _, release := range releases {
			release()
		}
		if resultErr != nil {
			if parentErr != nil {
				resultErr = errors.Join(resultErr, parentErr)
			} else if deadline, ok := shared.Deadline(); ok && !time.Now().Before(deadline) {
				resultErr = errors.Join(resultErr, context.DeadlineExceeded)
			}
		}
	}()
	started := map[string]bool{}
	active := map[string]bool{}
	running := 0
	start := func(protocol string) bool {
		if started[protocol] || shared.Err() != nil {
			return false
		}
		if running > 0 {
			if borrow, ok := ctx.Value(dnsTransportBudgetKey{}).(dnsTransportBorrow); ok {
				release, allowed := borrow()
				if !allowed {
					return false
				}
				releases = append(releases, release)
			}
		}
		started[protocol] = true
		active[protocol] = true
		running++
		workers.Add(1)
		go func() {
			defer workers.Done()
			selected := endpoint
			selected.Protocol = protocol
			at := time.Now()
			answer, err := exchangeWireAttempt(wireCtx, selected, query, true, 0, false)
			truncated := err == nil && repeatable && protocol == "udp" && len(answer) >= 12 && answer[2]&0x82 == 0x82 && answer[2]&0x78 == query[2]&0x78 && bytes.Equal(answer[:2], query[:2])
			if err == nil && !truncated && !matchingQuestion(query, answer) {
				err = &upstreamExchangeError{cause: errDNSResponseMismatch, protocol: protocol, phase: -1, attempts: 1}
			}
			results <- dnsTransportResult{protocol: protocol, body: answer, err: err, truncated: truncated, elapsed: time.Since(at)}
		}()
		return true
	}
	if !start(first) {
		return nil, shared.Err()
	}
	var timer *time.Timer
	var alarm <-chan time.Time
	if repeatable {
		if choice == 0 || choice == 3 {
			start(other)
		} else {
			latency := time.Duration((hint&transportDataMask)>>2) * time.Microsecond
			delay := max(750*time.Millisecond, latency+latency/2+50*time.Millisecond)
			timer = time.NewTimer(delay)
			alarm = timer.C
			defer timer.Stop()
		}
	}
	var last, tcpFailure error
	truncated := false
	for {
		select {
		case <-shared.Done():
			protocol := first
			if truncated {
				protocol = "tcp"
			} else if active[other] {
				protocol = other
			}
			return nil, &upstreamExchangeError{cause: shared.Err(), protocol: protocol, phase: -1, attempts: len(started)}
		case <-alarm:
			alarm = nil
			start(other)
		case result := <-results:
			running--
			active[result.protocol] = false
			if result.truncated {
				truncated = true
				// UDP has closed its socket; TCP can reuse the leader's
				// reservation if no overlapping attempt could be admitted.
				start("tcp")
			} else if result.err == nil {
				if preference != nil {
					preference.remember(result.protocol == "tcp", result.elapsed)
				}
				return result.body, nil
			} else {
				last = result.err
				if result.protocol == "tcp" {
					tcpFailure = result.err
				}
				if repeatable && running == 0 {
					start(other)
				}
			}
			if running == 0 {
				if truncated && tcpFailure != nil {
					return nil, tcpFailure
				}
				if last != nil {
					return nil, last
				}
				return nil, errDNSResponseMismatch
			}
		}
	}
}
