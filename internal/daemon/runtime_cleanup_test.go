package daemon

import (
	"errors"
	"testing"
	"time"
)

func TestRuntimeCleanupOverlapsIndependentClosuresAndJoinsErrors(t *testing.T) {
	coreStarted, dnsStarted, releaseDNS := make(chan struct{}), make(chan struct{}), make(chan struct{})
	coreErr, dnsErr := errors.New("core close"), errors.New("DNS restore")
	finished := make(chan error, 1)
	defer close(releaseDNS)
	go func() {
		finished <- closeCoreAndRestoreDNS(func() error {
			close(coreStarted)
			<-dnsStarted
			return coreErr
		}, func() error {
			close(dnsStarted)
			<-releaseDNS
			return dnsErr
		})
	}()
	for _, started := range []<-chan struct{}{coreStarted, dnsStarted} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("independent cleanup did not start concurrently")
		}
	}
	select {
	case <-finished:
		t.Fatal("cleanup returned before DNS restoration completed")
	default:
	}
	// Allow restoration to finish after checking that cleanup still waits.
	releaseDNS <- struct{}{}
	select {
	case err := <-finished:
		if !errors.Is(err, coreErr) || !errors.Is(err, dnsErr) {
			t.Fatalf("cleanup lost an error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish after both operations completed")
	}
}

func TestRuntimeCleanupWithoutDNSHandoff(t *testing.T) {
	want := errors.New("core close")
	calls := 0
	err := closeCoreAndRestoreDNS(func() error { calls++; return want }, nil)
	if calls != 1 || !errors.Is(err, want) {
		t.Fatalf("unexpected core cleanup: calls=%d err=%v", calls, err)
	}
}
