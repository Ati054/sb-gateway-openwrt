// Package dnsroute carries confirmed live selector identities within one daemon.
package dnsroute

import "sync"

type Core struct {
	PID        int
	StartTicks string
}

type Stamp struct {
	Epoch uint64
	Tag   string
}

// Registry is instance-owned. Callbacks run under its writer lock and must not
// perform network operations or call back into Registry. DNS cache operations
// use Read to make a lookup/insertion atomic with a route publication.
type Registry struct {
	mu         sync.RWMutex
	core       Core
	coreEpoch  uint64
	next       uint64
	routes     map[string]Stamp
	listeners  map[uint64]func(string, Stamp) func()
	listenerID uint64
}

func New(selectors []string) *Registry {
	r := &Registry{routes: make(map[string]Stamp), listeners: make(map[uint64]func(string, Stamp) func())}
	for _, id := range selectors {
		r.routes[id] = Stamp{}
	}
	return r
}

// SetCore withdraws all previous confirmations, including on PID reuse. Empty
// identity means recovery/shutdown; an old publisher can never revive it.
func (r *Registry) SetCore(core Core) {
	r.mu.Lock()
	if r.core == core {
		r.mu.Unlock()
		return
	}
	r.core, r.coreEpoch = core, r.coreEpoch+1
	var cleanup []func()
	for id := range r.routes {
		cleanup = append(cleanup, r.change(id, "")...)
	}
	r.mu.Unlock()
	for _, closeIdle := range cleanup {
		closeIdle()
	}
}

func (r *Registry) change(id, tag string) []func() {
	r.next++
	stamp := Stamp{Epoch: r.next, Tag: tag}
	r.routes[id] = stamp
	var cleanup []func()
	for _, changed := range r.listeners {
		if closeIdle := changed(id, stamp); closeIdle != nil {
			cleanup = append(cleanup, closeIdle)
		}
	}
	return cleanup
}

// Publisher captures one core incarnation before a readback transaction.
// Temporary probe selectors and unknown policies are never admitted.
func (r *Registry) Publisher(core Core) func(string, string) {
	r.mu.RLock()
	epoch := r.coreEpoch
	r.mu.RUnlock()
	return func(id, tag string) {
		r.mu.Lock()
		if core.PID <= 0 || core.StartTicks == "" || r.core != core || r.coreEpoch != epoch {
			r.mu.Unlock()
			return
		}
		old, ok := r.routes[id]
		if !ok || old.Tag == tag {
			r.mu.Unlock()
			return
		}
		cleanup := r.change(id, tag)
		r.mu.Unlock()
		for _, closeIdle := range cleanup {
			closeIdle()
		}
	}
}

// Generation also distinguishes an embedded core recreated in the same PID.
func (r *Registry) Generation(core Core) uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if core.PID <= 0 || core.StartTicks == "" || r.core != core {
		return 0
	}
	return r.coreEpoch
}

func (r *Registry) Read(id string, read func(Stamp)) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	read(r.routes[id])
}

// Subscribe also supplies current state atomically, avoiding startup gaps.
func (r *Registry) Subscribe(changed func(string, Stamp) func()) func() {
	r.mu.Lock()
	r.listenerID++
	id := r.listenerID
	r.listeners[id] = changed
	var cleanup []func()
	for policy, stamp := range r.routes {
		if closeIdle := changed(policy, stamp); closeIdle != nil {
			cleanup = append(cleanup, closeIdle)
		}
	}
	r.mu.Unlock()
	for _, closeIdle := range cleanup {
		closeIdle()
	}
	return func() { r.mu.Lock(); delete(r.listeners, id); r.mu.Unlock() }
}
