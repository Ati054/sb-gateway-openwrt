package dnsproxy

import "context"

// memoryGate accounts for estimated memory retained by each operation. Its
// capacity scales with router RAM; it is not a fixed device/request allowance.
// The listener waits for space instead of creating an unbounded goroutine queue
// or rejecting a burst as soon as 64 requests happen to overlap.
type memoryGate struct{ tokens chan struct{} }

func newMemoryGate(budget, perOperation int64) *memoryGate {
	return &memoryGate{tokens: make(chan struct{}, max(1, int(budget/perOperation)))}
}

func (m *memoryGate) acquire(ctx context.Context) error {
	select {
	case m.tokens <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *memoryGate) release() { <-m.tokens }
