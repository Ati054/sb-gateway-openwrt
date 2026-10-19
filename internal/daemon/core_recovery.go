package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/resources"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

// Ruleset publication and persistence form one transaction against recovery.
// Network downloads happen before taking the read lock.
var coreLifecycleMu sync.RWMutex
var errCoreRecovering = errors.New("Xray core is recovering")

const coreStablePeriod = 5 * time.Minute
const maxCoreRecoveryAttempts = 2

type coreRecoveryBudget struct {
	deadline    time.Time
	stableSince time.Time
	attempts    int
}

func (budget *coreRecoveryBudget) admit(now time.Time, window time.Duration) bool {
	if !budget.stableSince.IsZero() && now.Sub(budget.stableSince) >= coreStablePeriod {
		*budget = coreRecoveryBudget{}
	}
	if budget.deadline.IsZero() {
		budget.deadline = now.Add(window)
	}
	budget.stableSince = time.Time{}
	if !now.Before(budget.deadline) || budget.attempts >= maxCoreRecoveryAttempts {
		return false
	}
	budget.attempts++
	return true
}
func (budget *coreRecoveryBudget) verified(now time.Time) { budget.stableSince = now }

// API callers keep one stable runner. Withdrawing the current generation
// cancels in-flight calls before a new child is admitted. No second controller
// or telemetry cache is created, and the old child's Wait completes first.
type supervisedCore struct {
	onOOM           func()
	mu              sync.RWMutex
	current         *runningCore
	generation      context.Context
	cancel          context.CancelFunc
	ready           bool
	recoveringUntil time.Time
	closed          bool
}

func newSupervisedCore(core *runningCore) *supervisedCore {
	ctx, cancel := context.WithCancel(context.Background())
	return &supervisedCore{current: core, generation: ctx, cancel: cancel, ready: true}
}
func (supervisor *supervisedCore) Command(ctx context.Context, binary string, args ...string) ([]byte, error) {
	supervisor.mu.RLock()
	core, generation, ready := supervisor.current, supervisor.generation, supervisor.ready && !supervisor.closed
	supervisor.mu.RUnlock()
	if !ready {
		return nil, errCoreRecovering
	}
	request, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(generation, cancel)
	defer stop()
	defer cancel()
	return core.Command(request, binary, args...)
}
func (supervisor *supervisedCore) withdraw(deadline time.Time) {
	supervisor.mu.Lock()
	supervisor.ready = false
	supervisor.recoveringUntil = deadline
	supervisor.cancel()
	supervisor.mu.Unlock()
}
func (supervisor *supervisedCore) admit(core *runningCore) bool {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.closed {
		return false
	}
	supervisor.current = core
	supervisor.generation, supervisor.cancel = context.WithCancel(context.Background())
	supervisor.ready = true
	supervisor.recoveringUntil = time.Time{}
	return true
}
func (supervisor *supervisedCore) Recovering(now time.Time) bool {
	supervisor.mu.RLock()
	defer supervisor.mu.RUnlock()
	return !supervisor.closed && !supervisor.ready && !supervisor.recoveringUntil.IsZero() && now.Before(supervisor.recoveringUntil)
}
func (supervisor *supervisedCore) Close() error {
	supervisor.mu.Lock()
	supervisor.closed = true
	supervisor.ready = false
	supervisor.cancel()
	core := supervisor.current
	supervisor.mu.Unlock()
	return core.Close()
}

// Two restart attempts share one bounded window. Briefly responsive crash
// loops do not reset the allowance; five minutes of stable operation do.
// A hung controller still loses the independent procd watchdog lease.
func (supervisor *supervisedCore) run(ctx context.Context, window time.Duration, ready func(context.Context) error, withdraw func() error, restart func(context.Context) (*runningCore, error)) error {
	supervisor.mu.RLock()
	core := supervisor.current
	supervisor.mu.RUnlock()
	if core.exited == nil {
		<-ctx.Done()
		return ctx.Err()
	} // embedded diagnostic build
	budget := coreRecoveryBudget{stableSince: time.Now()}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	failedChecks := 0
	for {
		var cause error
		select {
		case <-ctx.Done():
			return ctx.Err()
		case cause = <-core.exited:
			if resources.KernelKilledForOOM(core.pid, core.startedBoot) {
				log.Printf("resproxy: Xray was killed by the kernel for out-of-memory (pid %d)", core.pid)
				supervisor.mu.RLock()
				onOOM := supervisor.onOOM
				supervisor.mu.RUnlock()
				if onOOM != nil {
					onOOM()
				}
			}
			if cause == nil {
				cause = errors.New("core exited unexpectedly")
			}
		case <-ticker.C:
			check, cancel := context.WithTimeout(ctx, 2*time.Second)
			cause = ready(check)
			cancel()
			if cause == nil {
				failedChecks = 0
				continue
			}
			failedChecks++
			if failedChecks < 3 {
				continue
			}
			// A saturated router can miss short control deadlines while Xray
			// continues forwarding. Confirm with a fresh, longer runtime probe
			// before destroying its established connections.
			confirm, confirmCancel := context.WithTimeout(ctx, 5*time.Second)
			cause = ready(confirm)
			confirmCancel()
			if cause == nil {
				failedChecks = 0
				continue
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !budget.admit(time.Now(), window) {
			supervisor.withdraw(time.Time{})
			_ = withdraw()
			return fmt.Errorf("Xray recovery allowance exhausted: %w", cause)
		}
		supervisor.withdraw(budget.deadline)
		if err := withdraw(); err != nil {
			return fmt.Errorf("withdraw Xray readiness: %w", err)
		}
		log.Printf("resproxy: Xray independent recovery attempt %d: %v", budget.attempts, cause)
		if err := core.Close(); err != nil {
			return err
		}
		recovery, cancel := context.WithDeadline(ctx, budget.deadline)
		replacement, err := restart(recovery)
		cancel()
		if err != nil {
			return fmt.Errorf("Xray independent recovery failed: %w", err)
		}
		if !supervisor.admit(replacement) {
			replacement.Close()
			return context.Canceled
		}
		core = replacement
		failedChecks = 0
		budget.verified(time.Now())
	}
}

// Reused by startup and recovery. Each selector is read back while blocked;
// the health controller alone admits a restored leaf after fresh proof.
func initializeCoreSelectors(ctx context.Context, manager *xray.Manager, selectors []string) error {
	for _, selector := range selectors {
		if err := manager.Restore(ctx, selector, "block"); err != nil {
			return fmt.Errorf("initialize fail-closed selector %s: %w", selector, err)
		}
	}
	return manager.Restore(ctx, xray.RefreshProbeTag, "block")
}
