package internet

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"
)

func TestDialerRetirementDrainsAndReclaimsLateUse(t *testing.T) {
	settings := &MemoryStreamConfig{}
	var closed atomic.Int32
	first, second := settings.AcquireDialer(), settings.AcquireDialer()
	settings.RegisterDialerCleanup("resource", func() { closed.Add(1) })
	settings.RetireDialers()
	first()
	first()
	if closed.Load() != 0 {
		t.Fatal("retirement closed a cache with a live connection")
	}
	second()
	if closed.Load() != 1 {
		t.Fatal("last stream did not reclaim the cache")
	}
	for range 1000 {
		release := settings.AcquireDialer()
		settings.RegisterDialerCleanup("resource", func() { closed.Add(1) })
		release()
	}
	if closed.Load() != 1001 || settings.dialers.active != 0 || len(settings.dialers.cleanups) != 0 {
		t.Fatal("late streams of a retired handler leaked resources")
	}
}

func TestDialerRetirementSerializesLateAcquireWithCleanup(t *testing.T) {
	settings := &MemoryStreamConfig{}
	started, finish := make(chan struct{}), make(chan struct{})
	release := settings.AcquireDialer()
	settings.RegisterDialerCleanup("resource", func() { close(started); <-finish })
	settings.RetireDialers()
	released := make(chan struct{})
	go func() { release(); close(released) }()
	<-started
	acquired := make(chan struct{})
	go func() { done := settings.AcquireDialer(); close(acquired); done() }()
	select {
	case <-acquired:
		t.Fatal("late stream acquired a cache while it was closing")
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	<-released
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("late stream did not resume after cleanup")
	}
}

func TestDialerRetirementConcurrentStreams(t *testing.T) {
	settings := &MemoryStreamConfig{}
	var live atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				release := settings.AcquireDialer()
				live.Add(1)
				settings.RegisterDialerCleanup("shared", func() {
					if live.Load() != 0 {
						t.Error("closed an in-use cache")
					}
				})
				live.Add(-1)
				release()
			}
		}()
	}
	settings.RetireDialers()
	wg.Wait()
	if settings.dialers.active != 0 || len(settings.dialers.cleanups) != 0 {
		t.Fatal("retired cache retained stream leases")
	}
}

func TestDialerRetirementDoesNotKeepConfigurationsAlive(t *testing.T) {
	makeRetired := func() weak.Pointer[MemoryStreamConfig] {
		settings := &MemoryStreamConfig{}
		release := settings.AcquireDialer()
		settings.RegisterDialerCleanup("self", func() { runtime.KeepAlive(settings) })
		settings.RetireDialers()
		release()
		return weak.Make(settings)
	}
	refs := make([]weak.Pointer[MemoryStreamConfig], 1000)
	for i := range refs {
		refs[i] = makeRetired()
	}
	// Live objects, not RSS: the Go allocator may retain reusable pages after
	// collection without leaking any configuration or transport client.
	runtime.GC()
	for _, ref := range refs {
		if ref.Value() != nil {
			t.Fatal("retirement retained an obsolete configuration")
		}
	}
}
