package dnsproxy

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/resources"
)

// Pending UDP recipients have no goroutine, TLS state or private response
// buffer. Only the leader owns an upstream operation. TCP connections retain
// their independently budgeted reader, with one outstanding query each.
const dnsRecipientMemory int64 = 512
const dnsLookupTimeout = 5 * time.Second

var errDNSAdmission = errors.New("DNS request memory budget exhausted")
var errDNSQueued = errors.New("DNS request canceled before upstream exchange")

type dnsRecipient struct {
	id   [2]byte
	done func([]byte, error)
}

type dnsJob struct {
	key, path  string
	ctx        context.Context
	query      []byte
	work       func(context.Context) ([]byte, error)
	recipients []dnsRecipient
	pending    int64
	active     bool
	stop       func() bool
	cancel     context.CancelFunc
}

type dnsPath struct {
	queue     []*dnsJob
	used      int64
	pending   int64
	executing int64
}

type dnsRequests struct {
	mu                         sync.Mutex
	owner                      *server
	jobs                       map[string]*dnsJob
	paths                      map[string]*dnsPath
	order                      []string
	next                       int
	budget, pendingLimit       int64
	used, pending              int64
	executing                  int64
	active, queued, recipients int64
	rejected                   uint64
	queueTimeouts              uint64
	closed                     bool
}

func (s *server) requests() *dnsRequests {
	s.requestsOnce.Do(func() {
		budget := s.requestBudget
		if budget == 0 {
			budget = dnsWorkBudget(resources.CurrentBudget().Total)
		}
		r := &dnsRequests{owner: s, jobs: make(map[string]*dnsJob), paths: make(map[string]*dnsPath), budget: budget, pendingLimit: budget / 2}
		for _, path := range []string{"local", "wan", "proxy:" + s.config.DefaultPolicy} {
			r.path(path)
		}
		for _, policy := range s.config.Policies {
			r.path("proxy:" + policy.ID)
		}
		for _, prefix := range s.prefixes {
			r.path("proxy:" + prefix.id)
		}
		s.dnsRequests = r
	})
	return s.dnsRequests
}

func (r *dnsRequests) path(name string) *dnsPath {
	if p := r.paths[name]; p != nil {
		return p
	}
	p := &dnsPath{}
	r.paths[name] = p
	r.order = append(r.order, name)
	return p
}

// Most capacity is shared. A small reservation for each configured path keeps
// a saturated slow resolver from blocking the first queries on another path.
// Tiny synthetic budgets that cannot hold one operation per path have no such
// reservation, but retain the same bounded queue and round-robin dispatch.
func (r *dnsRequests) permits(path string, bytes int64, pending bool) bool {
	if !pending {
		// Each pool already reserves capacity for other paths. Reserving it
		// again in the combined budget can reject a queued recipient even
		// while both its metadata pool and the total still have free space.
		return r.used+bytes <= r.budget
	}
	limit, used := r.pendingLimit, r.pending
	reserve := min(int64(4<<10), r.pendingLimit/int64(4*len(r.order)))
	if r.budget < int64(len(r.order))*2*(2*dnsRequestMemory+dnsRecipientMemory) {
		reserve = 0
	}
	for name, p := range r.paths {
		if name == path {
			continue
		}
		limit -= max(int64(0), reserve-p.pending)
	}
	return used+bytes <= limit
}

// Keep queue metadata from competing with TLS/response memory. Otherwise an
// early burst of running operations could fill the budget before later queries
// can join the bounded queue.
func (r *dnsRequests) canStart(path string) bool {
	limit := r.budget - r.pendingLimit
	reserve := min(int64(32<<10), limit/int64(2*len(r.order)))
	if r.budget < int64(len(r.order))*2*(2*dnsRequestMemory+dnsRecipientMemory) {
		reserve = 0
	}
	for name, p := range r.paths {
		if name != path {
			limit -= max(int64(0), reserve-p.executing)
		}
	}
	return r.executing+dnsRequestMemory <= limit && r.permits(path, dnsRequestMemory, false)
}

func (r *dnsRequests) submit(ctx context.Context, path, key string, query []byte, work func(context.Context) ([]byte, error), done func([]byte, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || ctx.Err() != nil {
		return context.Canceled
	}
	p := r.path(path)
	job := r.jobs[key]
	charge := dnsRecipientMemory
	if job == nil {
		charge += int64(2 * len(query))
	}
	if !r.permits(path, charge, true) || !r.permits(path, charge, false) {
		r.rejected++
		return errDNSAdmission
	}
	if job == nil {
		lookupCtx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
		job = &dnsJob{key: key, path: path, ctx: lookupCtx, query: query, work: work, cancel: cancel}
		r.jobs[key] = job
		p.queue = append(p.queue, job)
		r.queued++
		r.owner.workers.Add(1) // Includes queued leaders until completion/cancellation.
		job.stop = context.AfterFunc(lookupCtx, func() { r.cancelQueued(job) })
	}
	job.recipients = append(job.recipients, dnsRecipient{id: [2]byte{query[0], query[1]}, done: done})
	job.pending += charge
	r.pending += charge
	r.used += charge
	p.pending += charge
	p.used += charge
	r.recipients++
	r.dispatch()
	return nil
}

// Called under mu. Jobs own their goroutine only while actually executing;
// dispatch advances across paths instead of waiting behind a full slow path.
func (r *dnsRequests) dispatch() {
	if r.closed {
		return
	}
	for {
		started := false
		for i := 0; i < len(r.order); i++ {
			index := (r.next + i) % len(r.order)
			name := r.order[index]
			p := r.paths[name]
			if len(p.queue) == 0 || !r.canStart(name) {
				continue
			}
			job := p.queue[0]
			p.queue[0] = nil
			p.queue = p.queue[1:]
			job.active = true
			r.queued--
			r.active++
			r.used += dnsRequestMemory
			p.used += dnsRequestMemory
			r.executing += dnsRequestMemory
			p.executing += dnsRequestMemory
			r.next = (index + 1) % len(r.order)
			go func() {
				var body []byte
				err := job.ctx.Err()
				if err == nil {
					body, err = job.work(job.ctx)
				}
				r.complete(job, body, err)
			}()
			started = true
			break
		}
		if !started {
			return
		}
	}
}

func (r *dnsRequests) cancelQueued(job *dnsJob) {
	r.mu.Lock()
	if r.jobs[job.key] != job || job.active {
		r.mu.Unlock()
		return
	}
	r.removeQueued(job)
	if errors.Is(job.ctx.Err(), context.DeadlineExceeded) {
		r.queueTimeouts++
	}
	r.mu.Unlock()
	r.deliver(job, nil, errDNSQueued)
}

func (r *dnsRequests) removeQueued(job *dnsJob) {
	delete(r.jobs, job.key)
	p := r.paths[job.path]
	for i, entry := range p.queue {
		if entry == job {
			copy(p.queue[i:], p.queue[i+1:])
			p.queue[len(p.queue)-1] = nil
			p.queue = p.queue[:len(p.queue)-1]
			break
		}
	}
	r.queued--
}

func (r *dnsRequests) complete(job *dnsJob, body []byte, err error) {
	r.mu.Lock()
	delete(r.jobs, job.key)
	r.mu.Unlock()
	r.deliver(job, body, err)
}

func (r *dnsRequests) deliver(job *dnsJob, body []byte, err error) {
	if job.stop != nil {
		job.stop()
	}
	job.cancel()
	if err != nil {
		body = servfail(job.query)
	}
	// Keep the memory charge while sending responses. Each callback receives
	// its own bytes; transaction IDs and TCP buffers can never alias.
	for _, recipient := range job.recipients {
		response := append([]byte(nil), body...)
		if len(response) >= 2 {
			copy(response[:2], recipient.id[:])
		}
		recipient.done(response, err)
	}
	r.mu.Lock()
	p := r.paths[job.path]
	r.pending -= job.pending
	p.pending -= job.pending
	charge := job.pending
	if job.active {
		charge += dnsRequestMemory
		r.active--
		r.executing -= dnsRequestMemory
		p.executing -= dnsRequestMemory
	}
	r.used -= charge
	p.used -= charge
	r.recipients -= int64(len(job.recipients))
	r.dispatch()
	r.mu.Unlock()
	r.owner.workers.Done()
}

func (r *dnsRequests) close() {
	r.mu.Lock()
	r.closed = true
	var queued []*dnsJob
	for _, job := range r.jobs {
		if !job.active {
			r.removeQueued(job)
			queued = append(queued, job)
		}
	}
	r.mu.Unlock()
	for _, job := range queued {
		r.deliver(job, nil, errDNSQueued)
	}
}

func (r *dnsRequests) stats() map[string]uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return map[string]uint64{"active_upstream": uint64(r.active), "queued_upstream": uint64(r.queued), "waiting_shared": uint64(max(int64(0), r.recipients-r.active-r.queued)), "pending_query_bytes": uint64(r.pending), "dns_work_bytes": uint64(r.used), "dns_admission_rejected": r.rejected, "dns_queue_timeouts": r.queueTimeouts}
}
