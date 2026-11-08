package dnsproxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestDoHHTTP2RetriesOnFreshConnectionWithoutAbortingOtherStream(t *testing.T) {
	var mu sync.Mutex
	var original string
	var faultRequests int
	held, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		if r.ProtoMajor != 2 {
			t.Error("test did not negotiate HTTP/2")
			return
		}
		mu.Lock()
		if original == "" {
			original = r.RemoteAddr
		}
		old := r.RemoteAddr == original
		if bytes.Contains(q, []byte("broken")) {
			faultRequests++
		}
		mu.Unlock()
		if bytes.Contains(q, []byte("held")) {
			close(held)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if old && bytes.Contains(q, []byte("broken")) {
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(dohReply(q))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	defer unblock()
	transport := server.Client().Transport.(*http.Transport).Clone()
	client := newDoHClient(transport)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := exchangeDoH(ctx, client, server.URL, dnsQuery("warm.example")); err != nil {
		t.Fatal(err)
	}
	if client.Transport.(*recoveringTransport).current != transport {
		t.Fatal("a healthy HTTP/2 response discarded the connection pool")
	}
	heldResult := make(chan error, 1)
	go func() { _, err := exchangeDoH(ctx, client, server.URL, dnsQuery("held.example")); heldResult <- err }()
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("healthy stream did not start")
	}
	if _, err := exchangeDoH(ctx, client, server.URL, dnsQuery("broken.example")); err != nil {
		t.Fatalf("transient HTTP/2 failure reached client: %v", err)
	}
	mu.Lock()
	requests := faultRequests
	mu.Unlock()
	if requests != 2 {
		t.Fatalf("want one retry, got %d attempts", requests)
	}
	if client.Transport.(*recoveringTransport).current == transport {
		t.Fatal("retry reused the failed transport")
	}
	// Let the already running stream finish through its original transport.
	// A canceled healthy stream also makes the old server connection disappear.
	select {
	case err := <-heldResult:
		t.Fatalf("healthy stream ended during recovery: %v", err)
	default:
	}
	unblock()
	select {
	case err := <-heldResult:
		if err != nil {
			t.Fatalf("healthy stream lost: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("healthy stream did not finish")
	}
}

func TestDoHHTTP2PersistentFailureStillStopsAfterOneRetry(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}
	})}
	_, err := exchangeDoH(t.Context(), client, "https://resolver.example/dns-query", dnsQuery("query.example"))
	var stream http2.StreamError
	if !errors.As(err, &stream) || calls != 2 {
		t.Fatalf("attempts=%d, error=%v", calls, err)
	}
}

func TestDoHHTTP2RetryPolicy(t *testing.T) {
	for _, code := range []http2.ErrCode{http2.ErrCodeProtocol, http2.ErrCodeInternal, http2.ErrCodeRefusedStream, http2.ErrCodeCancel} {
		if !retryDoH(http2.StreamError{Code: code}) {
			t.Fatalf("transient code not retried: %s", code)
		}
	}
	for _, code := range []http2.ErrCode{http2.ErrCodeEnhanceYourCalm, http2.ErrCodeInadequateSecurity, http2.ErrCodeHTTP11Required} {
		if retryDoH(http2.StreamError{Code: code}) {
			t.Fatalf("non-transient rejection retried: %s", code)
		}
	}
	if retryDoH(context.Canceled) || retryDoH(context.DeadlineExceeded) || retryDoH(&net.DNSError{IsTimeout: true}) {
		t.Fatal("retry ignored cancellation or shared deadline")
	}
}

func TestDoHNonQueryDoesNotReplayAfterTransportError(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, io.EOF })}
	q := dnsQuery("update.example")
	q[2] |= 5 << 3
	if _, err := exchangeDoH(t.Context(), client, "https://resolver.example/dns-query", q); err == nil || calls != 1 {
		t.Fatal("non-query DNS operation was replayed")
	}
}

func TestDoHLateConcurrentFailuresDoNotRetireReplacement(t *testing.T) {
	old := &http.Transport{}
	owner := &recoveringTransport{current: old}
	owner.retire(old)
	replacement := owner.current
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() { owner.retire(old) })
	}
	group.Wait()
	if owner.current != replacement {
		t.Fatal("late errors kept cloning healthy transport")
	}
	owner.CloseIdleConnections()
	owner.retire(replacement)
	if owner.current != replacement {
		t.Fatal("shutdown resurrected a transport")
	}
}

func TestDoHCanceledBodyDoesNotRotateHealthyTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	transport := &http.Transport{}
	owner := &recoveringTransport{current: transport}
	body := &drainingBody{ReadCloser: io.NopCloser(errorReader{}), owner: owner, transport: transport, ctx: ctx}
	_, _ = body.Read(make([]byte, 1))
	_ = body.Close()
	if owner.current != transport {
		t.Fatal("client cancellation discarded healthy pool")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
