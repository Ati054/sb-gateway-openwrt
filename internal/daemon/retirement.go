package daemon

import (
	"context"
	"errors"
	"time"
)

const outboundDrainGrace = 30 * time.Minute
const maxDrainingOutbounds = 8

type outboundRetirer interface {
	Current(context.Context, string) (string, error)
	Retire(context.Context, string, string) error
}

type retiredOutbound struct {
	tag        string
	addedAt    time.Time
	retryAfter time.Time
}

type retirementQueue struct{ entries []retiredOutbound }

// Observe keeps the previous handler available for existing connections.
// Frequent endpoint churn is bounded to eight draining handlers; the oldest
// Older generations are retired after a subsequent update; the patched Xray
// core keeps already-open streams alive while their transport drains.
func (q *retirementQueue) Observe(tag string, now time.Time) {
	if tag == "" || tag == "block" || tag == "direct-wan" {
		return
	}
	for _, entry := range q.entries {
		if entry.tag == tag {
			return
		}
	}
	q.entries = append(q.entries, retiredOutbound{tag: tag, addedAt: now})
}

func (q *retirementQueue) Sweep(ctx context.Context, now time.Time, selector string, runtime outboundRetirer) error {
	if runtime == nil || selector == "" {
		return errors.New("outbound retirement runtime is unavailable")
	}
	var failures []error
	for index, budget := 0, 4; index < len(q.entries) && budget > 0; {
		entry := &q.entries[index]
		due := now.Sub(entry.addedAt) >= outboundDrainGrace || index < len(q.entries)-1
		if !due || now.Before(entry.retryAfter) {
			index++
			continue
		}
		budget--
		current, err := runtime.Current(ctx, selector)
		if err != nil {
			entry.retryAfter = now.Add(time.Minute)
			failures = append(failures, err)
			index++
			continue
		}
		if current == entry.tag {
			entry.addedAt = now // It became active again; restart its drain grace.
			index++
			continue
		}
		if err := runtime.Retire(ctx, selector, entry.tag); err != nil {
			entry.retryAfter = now.Add(time.Minute)
			failures = append(failures, err)
			index++
			continue
		}
		q.entries = append(q.entries[:index], q.entries[index+1:]...)
	}
	return errors.Join(failures...)
}

func (q *retirementQueue) HasCapacity() bool { return len(q.entries) < maxDrainingOutbounds }
