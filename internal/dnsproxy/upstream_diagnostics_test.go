package dnsproxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func TestUpstreamErrorDiagnosticsAreBoundedAndPrivate(t *testing.T) {
	s := &server{config: Config{StatusPath: filepath.Join(t.TempDir(), "dns-status.json")}}
	for i, scenario := range []struct {
		err  error
		kind string
	}{
		{context.DeadlineExceeded, "timeout"}, {context.Canceled, "canceled"},
		{io.EOF, "eof"}, {io.ErrUnexpectedEOF, "eof"}, {syscall.ECONNRESET, "reset"},
		{syscall.ECONNREFUSED, "refused"}, {syscall.EPIPE, "broken_pipe"}, {syscall.ENETUNREACH, "unreachable"},
		{tls.RecordHeaderError{}, "tls"}, {upstreamResponseError{status: 429}, "http_response"},
		{errDNSResponseMismatch, "response_mismatch"}, {errDNSUpstreamClosed, "closed"},
		{errors.New("http2: stream reset PRIVATE-DOMAIN-CANARY"), "http2"},
		{errors.New("PRIVATE-DOMAIN-CANARY"), "other"},
	} {
		err := &url.Error{Op: "Post", URL: "https://PRIVATE-DOMAIN-CANARY/private-token", Err: scenario.err}
		if got := upstreamFailureNames[upstreamFailureCause(err)]; got != scenario.kind {
			t.Fatalf("%d: %s, want %s", i, got, scenario.kind)
		}
		s.upstreamErrors.record(i%2 == 0, "doh", err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				s.upstreamErrors.record(false, "PRIVATE-DOMAIN-CANARY", syscall.ECONNRESET)
			}
		}()
	}
	wg.Wait()
	if err := s.writeUpstreamErrors(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(s.upstreamErrorsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "PRIVATE") || len(body) > 2000 {
		t.Fatalf("private or unbounded diagnostic: %s", body)
	}
	info, _ := os.Stat(s.upstreamErrorsPath())
	if info.Mode().Perm() != 0600 {
		t.Fatal("diagnostics are not private")
	}
	if err := s.writeUpstreamErrors(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(s.upstreamErrorsPath())
	if !os.SameFile(info, after) {
		t.Fatal("unchanged diagnostics were rewritten")
	}
	var report map[string]struct {
		Failed uint64            `json:"failed_exchanges"`
		Causes map[string]uint64 `json:"causes"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if report["proxy"].Causes["reset"] != 16000 || report["wan"].Failed == 0 {
		t.Fatalf("path accounting: %s", body)
	}
}

func TestSharedDNSFailureIsOneFailedUpstreamExchange(t *testing.T) {
	gate := make(chan struct{})
	var calls int
	client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { calls++; <-gate; return nil, syscall.ECONNREFUSED })}
	s := &server{direct: client, config: Config{DefaultPolicy: "main", DirectURL: "https://resolver.example/dns-query"}}
	results := make(chan []byte, 16)
	for id := range 16 {
		q := dnsQuery("same.example")
		q[1] = byte(id)
		s.resolveAsync(context.Background(), q, netip.MustParseAddr("127.0.0.1"), func(body []byte) { results <- body })
	}
	close(gate)
	for range 16 {
		if r := <-results; r[3]&15 != 2 {
			t.Fatal("failed exchange did not reach all recipients")
		}
	}
	if calls != 1 || s.dnsFailures.Load() != 16 {
		t.Fatalf("calls=%d, failed requests=%d", calls, s.dnsFailures.Load())
	}
	if s.upstreamErrors.paths[1].counts[4] != 1 {
		t.Fatal("shared recipients multiplied the upstream failure")
	}
}

func TestDoHErrorMetadataPreservesRetryAndCause(t *testing.T) {
	var calls int
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		trace := httptrace.ContextClientTrace(r.Context())
		trace.GotConn(httptrace.GotConnInfo{Reused: true})
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		return nil, io.EOF
	})}
	_, err := exchangeDoH(context.Background(), client, "https://PRIVATE-DOMAIN-CANARY/dns-query", dnsQuery("private.example"))
	var detail *upstreamExchangeError
	if !errors.Is(err, io.EOF) || !errors.As(err, &detail) || calls != 2 || detail.attempts != 2 || detail.phase != 3 || !detail.reused {
		t.Fatalf("lost metadata or changed retry: %v, %+v", err, detail)
	}
	var diagnostics upstreamFailureDiagnostics
	diagnostics.record(false, "doh", err)
	if diagnostics.paths[0].last.Attempts != 2 || diagnostics.paths[0].last.Phase != "headers" {
		t.Fatal("failed DoH attempt details missing")
	}
}
