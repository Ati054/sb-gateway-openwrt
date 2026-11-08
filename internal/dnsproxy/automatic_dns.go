package dnsproxy

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

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
	// Zone transfers have streaming semantics and cannot be retried as an
	// ordinary one-response lookup (IXFR=251, AXFR=252).
	return err == nil && question.Type != 251 && question.Type != 252
}

// Try transports sequentially: one admitted DNS job still owns at most one
// upstream socket. All attempts share the existing five-second deadline.
// Remember a working transport; do not probe UDP on every TCP-served request.
func exchangeAutomaticDNS(ctx context.Context, endpoint Endpoint, query []byte, tcpPreferred *atomic.Bool) (response []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if err == nil {
			return
		}
		if contextErr := ctx.Err(); contextErr != nil {
			err = errors.Join(err, contextErr)
		} else if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			// A socket deadline can fire before the context timer is scheduled.
			// Keep the wire cause for diagnostics and expose the shared deadline.
			err = errors.Join(err, context.DeadlineExceeded)
		}
	}()
	first := "udp"
	if tcpPreferred != nil && tcpPreferred.Load() {
		first = "tcp"
	}
	other := "tcp"
	if first == "tcp" {
		other = "udp"
	}
	transports := []string{first}
	if repeatableDNSQuery(query) {
		// A final attempt gives a slow preferred transport the remaining
		// budget when the other transport is blocked too.
		transports = []string{first, other, first}
	}
	var last error
	retryFirst := true
	for index, protocol := range transports {
		if err := ctx.Err(); err != nil {
			if last != nil {
				return nil, last
			}
			return nil, err
		}
		deadline, _ := ctx.Deadline()
		budget := time.Until(deadline)
		if len(transports) > 1 {
			switch index {
			case 0:
				budget = min(budget/4, 750*time.Millisecond)
			case 1:
				if retryFirst {
					budget = min(budget/3, 1500*time.Millisecond)
				}
			}
		}
		attempt, cancelAttempt := context.WithTimeout(ctx, budget)
		selected := endpoint
		selected.Protocol = protocol
		response, err := exchangeWire(attempt, selected, query, true, 0)
		cancelAttempt()
		if err == nil && !matchingQuestion(query, response) {
			err = errDNSResponseMismatch
		}
		if err == nil {
			if tcpPreferred != nil {
				tcpPreferred.Store(protocol == "tcp")
			}
			return response, nil
		}
		last = err
		var wireFailure *upstreamExchangeError
		if protocol == "udp" && errors.As(err, &wireFailure) && wireFailure.protocol == "tcp" {
			// UDP explicitly requested TCP with TC=1. Repeating UDP cannot
			// replace the failed TCP exchange for this response.
			return nil, err
		}
		if index == 0 {
			retryFirst = timeoutError(err)
		} else if index == 1 && !retryFirst {
			return nil, err
		}
	}
	return nil, last
}
