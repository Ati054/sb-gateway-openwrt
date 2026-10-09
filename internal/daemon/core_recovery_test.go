package daemon

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoreRecoveryAllowanceIsBoundedAcrossVerifiedCrashLoop(t *testing.T) {
	now := time.Unix(1000, 0)
	budget := coreRecoveryBudget{}
	if !budget.admit(now, 30*time.Second) {
		t.Fatal("first recovery denied")
	}
	deadline := budget.deadline
	budget.verified(now.Add(time.Second))
	if !budget.admit(now.Add(2*time.Second), 30*time.Second) || budget.deadline != deadline {
		t.Fatal("verification extended recovery window")
	}
	budget.verified(now.Add(3 * time.Second))
	if budget.admit(now.Add(4*time.Second), 30*time.Second) {
		t.Fatal("crash loop not bounded")
	}
	budget.verified(now.Add(5 * time.Second))
	if !budget.admit(now.Add(5*time.Second+coreStablePeriod), 30*time.Second) {
		t.Fatal("stable core did not regain allowance")
	}
	fresh := coreRecoveryBudget{}
	if !fresh.admit(now, time.Second) || fresh.admit(now.Add(time.Second), time.Second) {
		t.Fatal("deadline not enforced")
	}
}
func TestCoreWithdrawalCancelsInflightAndRejectsNewCalls(t *testing.T) {
	started := make(chan struct{})
	core := &runningCore{command: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, close: func() error { return nil }}
	supervisor := newSupervisedCore(core)
	defer supervisor.Close()
	done := make(chan error, 1)
	go func() { _, err := supervisor.Command(context.Background(), "ignored"); done <- err }()
	<-started
	supervisor.withdraw(time.Now().Add(time.Second))
	if !supervisor.Recovering(time.Now()) {
		t.Fatal("bounded recovery not advertised")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("inflight call survived old generation")
	}
	if _, err := supervisor.Command(context.Background(), "ignored"); !errors.Is(err, errCoreRecovering) {
		t.Fatal("new call admitted during withdrawal")
	}
	supervisor.withdraw(time.Time{})
	if supervisor.Recovering(time.Now()) {
		t.Fatal("unbounded watchdog grace")
	}
}
func TestIndependentCoreCrashLoopWithdrawsBeforeRestartAndEscalates(t *testing.T) {
	var closed atomic.Int32
	makeCore := func() *runningCore {
		done := make(chan error, 1)
		done <- errors.New("crash")
		return &runningCore{exited: done, command: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }, close: func() error { closed.Add(1); return nil }}
	}
	supervisor := newSupervisedCore(makeCore())
	defer supervisor.Close()
	withdrawals, restarts := 0, 0
	err := supervisor.run(context.Background(), time.Second, func(context.Context) error { return nil }, func() error { withdrawals++; return nil }, func(context.Context) (*runningCore, error) {
		if withdrawals != restarts+1 || closed.Load() != int32(restarts+1) {
			t.Fatal("restart before withdrawal/reap")
		}
		restarts++
		return makeCore(), nil
	})
	if err == nil || !strings.Contains(err.Error(), "allowance exhausted") || restarts != 2 || withdrawals != 3 {
		t.Fatalf("recovery loop: %v restarts=%d withdrawals=%d", err, restarts, withdrawals)
	}
	if supervisor.Recovering(time.Now()) {
		t.Fatal("failed recovery kept watchdog grace")
	}
}
func TestCoreRecoveryFailureAndCancellationRemainFatal(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancel"}[cancelled], func(t *testing.T) {
			exited := make(chan error, 1)
			exited <- errors.New("crash")
			core := &runningCore{exited: exited, close: func() error { return nil }}
			supervisor := newSupervisedCore(core)
			defer supervisor.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := supervisor.run(ctx, time.Second, func(context.Context) error { return nil }, func() error { return nil }, func(recovery context.Context) (*runningCore, error) {
				if cancelled {
					cancel()
					<-recovery.Done()
					return nil, recovery.Err()
				}
				return nil, errors.New("invalid config")
			})
			if err == nil {
				t.Fatal("failed restart was hidden")
			}
		})
	}
}
