package internet

import "sync"

// A removed outbound may still have live streams or a Dispatch which already
// obtained its handler. Retire the cache only after those streams release it.
// The state belongs to the stream configuration, never to a global registry.
type dialerLifetime struct {
	mu       sync.Mutex
	active   int
	retired  bool
	cleaning chan struct{}
	cleanups map[any]func()
}

// AcquireDialer keeps transport resources alive until the returned function is
// called. Late users of an already removed handler get their own cleanup cycle.
func (s *MemoryStreamConfig) AcquireDialer() func() {
	d := &s.dialers
	d.mu.Lock()
	for d.cleaning != nil {
		finished := d.cleaning
		d.mu.Unlock()
		<-finished
		d.mu.Lock()
	}
	d.active++
	d.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { d.finish(false) }) }
}

// RegisterDialerCleanup is called while holding a dialer lease and before
// acquiring a transport cache lock. A key is registered only once per cycle.
func (s *MemoryStreamConfig) RegisterDialerCleanup(key any, cleanup func()) {
	d := &s.dialers
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cleanups == nil {
		d.cleanups = make(map[any]func())
	}
	if _, exists := d.cleanups[key]; !exists {
		d.cleanups[key] = cleanup
	}
}

// RetireDialers prevents idle caches from keeping an obsolete outbound alive.
// Active streams drain normally; this is deliberately different from Close.
func (s *MemoryStreamConfig) RetireDialers() {
	if s != nil {
		s.dialers.finish(true)
	}
}

func (d *dialerLifetime) finish(retire bool) {
	d.mu.Lock()
	if retire {
		d.retired = true
	} else {
		d.active--
	}
	if !d.retired || d.active != 0 || d.cleaning != nil || len(d.cleanups) == 0 {
		d.mu.Unlock()
		return
	}
	cleanups := d.cleanups
	d.cleanups = nil
	d.cleaning = make(chan struct{})
	d.mu.Unlock()
	// No user can acquire this cache while its old connections are closing.
	// Run callbacks outside the lifetime lock to avoid transport lock inversion.
	for _, cleanup := range cleanups {
		cleanup()
	}
	d.mu.Lock()
	close(d.cleaning)
	d.cleaning = nil
	d.mu.Unlock()
}
