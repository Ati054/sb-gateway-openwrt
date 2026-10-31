package agentport

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

var qualityWaveSequence atomic.Uint64

// Preparation is outside the HTTPS measurement. A stuck peer must yield the
// batch rather than start a comparison against an already completed request.
const qualityPreparationTimeout = 5 * time.Second

type qualityStartBarrier struct {
	id     uint64
	left   atomic.Int32
	ready  chan struct{}
	cancel context.CancelFunc
}

type qualityStartTicket struct {
	barrier *qualityStartBarrier
	once    sync.Once
}

func (ticket *qualityStartTicket) arrive() {
	if ticket != nil {
		ticket.once.Do(func() {
			if ticket.barrier.left.Add(-1) == 0 {
				close(ticket.barrier.ready)
			}
		})
	}
}

func (ticket *qualityStartTicket) wait(ctx context.Context) error {
	if ticket == nil {
		return nil
	}
	ticket.arrive()
	timer := time.NewTimer(qualityPreparationTimeout)
	defer timer.Stop()
	select {
	case <-ticket.barrier.ready:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		ticket.barrier.cancel()
		return errProbeSchedulingDelayed
	}
}

type probeLaneJob struct {
	node  string
	start *qualityStartTicket
}

// Availability keeps its first-result worker pool. Quality assigns one peer to
// each lane and gates each physical wave after selector preparation.
func probeLaneJobs(candidates []string, workers int, quality bool, cancel context.CancelFunc) []<-chan probeLaneJob {
	queues := make([]<-chan probeLaneJob, workers)
	if !quality {
		jobs := make(chan probeLaneJob, len(candidates))
		for _, candidate := range candidates {
			jobs <- probeLaneJob{node: candidate}
		}
		close(jobs)
		for i := range queues {
			queues[i] = jobs
		}
		return queues
	}
	lanes := make([]chan probeLaneJob, workers)
	for i := range lanes {
		lanes[i] = make(chan probeLaneJob, (len(candidates)+workers-1)/workers)
		queues[i] = lanes[i]
	}
	var barrier *qualityStartBarrier
	for i, candidate := range candidates {
		if i%workers == 0 {
			barrier = &qualityStartBarrier{id: qualityWaveSequence.Add(1), ready: make(chan struct{}), cancel: cancel}
			barrier.left.Store(int32(minInt(workers, len(candidates)-i)))
		}
		lanes[i%workers] <- probeLaneJob{node: candidate, start: &qualityStartTicket{barrier: barrier}}
	}
	for _, lane := range lanes {
		close(lane)
	}
	return queues
}

func qualityProbeWidth(runtime selectorRuntime) int {
	if runtime, ok := runtime.(interface{ qualityProbeParallelism() int }); ok {
		return maxInt(1, runtime.qualityProbeParallelism())
	}
	return 1
}

func qualityIntervalsOverlap(active, candidate probeEvidence) bool {
	return !active.QualityStartedAt.IsZero() && !candidate.QualityStartedAt.IsZero() &&
		active.QualityStartedAt.Before(active.ObservedAt) && candidate.QualityStartedAt.Before(candidate.ObservedAt) &&
		active.QualityStartedAt.Before(candidate.ObservedAt) && candidate.QualityStartedAt.Before(active.ObservedAt)
}
