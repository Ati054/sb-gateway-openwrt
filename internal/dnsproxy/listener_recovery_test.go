package dnsproxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

type resourceLimitedDNSListener struct {
	net.Listener
	remaining int
}

func (l *resourceLimitedDNSListener) Accept() (net.Conn, error) {
	if l.remaining > 0 {
		l.remaining--
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}
	}
	return l.Listener.Accept()
}

func TestDNSListenerRecoversAfterTemporaryDescriptorExhaustion(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	s := rejectedHeaderFixture(t)
	s.clientMemory = newMemoryGate(2*dnsClientMemory, dnsClientMemory)
	done := make(chan struct{})
	go func() { defer close(done); s.serveTCP(ctx, &resourceLimitedDNSListener{listener, 2}) }()
	defer func() { cancel(); listener.Close(); <-done; s.workers.Wait() }()
	conn, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	query := dnsQuery("listener-recovery.example.test")
	query[2] = 2 << 3 // STATUS receives a local NOTIMP, with no upstream fixture.
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(query)))
	if _, err := conn.Write(append(frame, query...)); err != nil {
		t.Fatal(err)
	}
	var response [14]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		t.Fatalf("temporary resource pressure permanently disabled DNS/TCP: %v", err)
	}
	if binary.BigEndian.Uint16(response[:2]) != 12 || response[5]&15 != 4 {
		t.Fatalf("unexpected local DNS reply: %x", response)
	}
}

func TestDNSAcceptRetryStopsOnShutdownAndPermanentFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	temporary := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}
	delay := time.Second
	if retryDNSAccept(ctx, temporary, &delay) {
		t.Fatal("canceled listener retried")
	}
	if retryDNSAccept(t.Context(), net.ErrClosed, &delay) {
		t.Fatal("closed listener retried")
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan bool, 1)
	go func() { finished <- retryDNSAccept(ctx, temporary, &delay) }()
	cancel()
	select {
	case retried := <-finished:
		if retried {
			t.Fatal("shutdown did not cancel accept backoff")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("shutdown waited for accept retry timer")
	}
}
