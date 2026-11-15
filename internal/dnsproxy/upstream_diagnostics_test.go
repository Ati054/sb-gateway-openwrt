package dnsproxy

import (
	"bytes"
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
	"sync/atomic"
	"syscall"
	"testing"
)

func TestDoHBadRequestDiagnosticsDistinguishQuestionlessQueries(t *testing.T) {
	var calls atomic.Int32
	s, publish := managedFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		http.Error(w, "Invalid query.", http.StatusBadRequest)
	})
	s.config.StatusPath = filepath.Join(t.TempDir(), "dns-status.json")
	publish("resproxy-main", "working")
	queries := [][]byte{questionlessQuery(false), questionlessQuery(true), dnsQuery("PRIVATE-DOMAIN-CANARY.example")}
	for _, query := range queries {
		reply := s.resolve(t.Context(), query, netip.MustParseAddr("192.0.2.8"))
		if len(reply) < 12 || reply[3]&15 != 2 {
			t.Fatal("HTTP400 did not return SERVFAIL")
		}
		if err := s.writeUpstreamErrors(); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(s.upstreamErrorsPath())
		if err != nil {
			t.Fatal(err)
		}
		var report map[string]struct {
			Last         upstreamFailureSample `json:"last"`
			Failed       uint64                `json:"failed_exchanges"`
			Questionless uint64                `json:"questionless_failed_exchanges"`
		}
		if err := json.Unmarshal(body, &report); err != nil {
			t.Fatal(err)
		}
		last := report["proxy"].Last
		if last.Status != 400 || last.Query == nil || last.Query.Bytes != len(query) || last.Query.Questions != uint16(query[5]) || last.Query.Additionals != uint16(query[11]) {
			t.Fatalf("missing query metadata: %s", body)
		}
		if strings.Contains(string(body), "PRIVATE") || bytes.Contains(body, []byte("192.0.2.8")) || len(body) > 2000 {
			t.Fatal("unbounded or private failure metadata")
		}
		if report["proxy"].Questionless != min(report["proxy"].Failed, 2) || report["wan"].Failed != 0 {
			t.Fatalf("incorrect path/probe accounting: %s", body)
		}
	}
	if calls.Load() != 3 || s.dnsFailures.Load() != 3 || s.failureInvalidQuery.Load() != 0 {
		t.Fatal("diagnostics changed retry, validation, or failure accounting")
	}
}

func TestUpstreamQueryMetadataDoesNotRetainWireStorage(t *testing.T) {
	query := questionlessQuery(true)
	var diagnostic upstreamFailureDiagnostics
	diagnostic.record(false, "doh", upstreamResponseError{status: 400}, query)
	for i := range query {
		query[i] = 0xff
	}
	last := diagnostic.paths[0].last.Query
	if last == nil || last.Bytes != 23+12 || last.Questions != 0 || last.Additionals != 1 {
		t.Fatalf("query metadata aliased mutable packet: %+v", last)
	}
}

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
	if strings.Contains(string(body), "PRIVATE") || len(body) > 12000 {
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

func TestUpstreamFailureHistoryRetainsBoundedIndependentPaths(t *testing.T) {
	s := &server{config: Config{StatusPath: filepath.Join(t.TempDir(), "dns-status.json")}}
	for i := range 3 * upstreamFailureCapacity {
		query := dnsQuery("PRIVATE-DOMAIN-CANARY.example")
		query[2] |= byte(i%2) << 3
		query[11] = byte(i)
		s.upstreamErrors.record(false, "doh", upstreamResponseError{status: 400 + i}, query)
		clear(query)
	}
	s.upstreamErrors.record(true, "udp", syscall.ECONNREFUSED, questionlessQuery(false))
	if err := s.writeUpstreamErrors(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(s.upstreamErrorsPath())
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]struct {
		Failed uint64                  `json:"failed_exchanges"`
		Last   upstreamFailureSample   `json:"last"`
		Recent []upstreamFailureSample `json:"recent_samples"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	proxy, wan := report["proxy"], report["wan"]
	if proxy.Failed != 3*upstreamFailureCapacity || len(proxy.Recent) != upstreamFailureCapacity || wan.Failed != 1 || len(wan.Recent) != 1 {
		t.Fatalf("unbounded or mixed paths: %s", body)
	}
	for i, sample := range proxy.Recent {
		original := 2*upstreamFailureCapacity + i
		if sample.Status != 400+original || sample.Query == nil || sample.Query.Opcode != uint16(original%2) || sample.Query.Additionals != uint16(original) {
			t.Fatalf("history lost order or retained mutable input: %+v", sample)
		}
	}
	if proxy.Last.Status != proxy.Recent[len(proxy.Recent)-1].Status || wan.Last.Cause != "refused" || strings.Contains(string(body), "PRIVATE") || len(body) > 12000 {
		t.Fatal("history changed last sample, retained names, or exceeded its size bound")
	}
}

func TestUpstreamFailureHistoryPublishesDuringConcurrentTraffic(t *testing.T) {
	s := &server{config: Config{StatusPath: filepath.Join(t.TempDir(), "dns-status.json")}}
	var wg sync.WaitGroup
	for path := range 2 {
		wg.Go(func() {
			for range 100 {
				s.upstreamErrors.record(path == 1, "doh", io.EOF, questionlessQuery(true))
			}
		})
	}
	for range 10 {
		if err := s.writeUpstreamErrors(); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if err := s.writeUpstreamErrors(); err != nil {
		t.Fatal(err)
	}
	for _, path := range s.upstreamErrors.paths {
		if path.counts[2] != 100 || path.recentCount != upstreamFailureCapacity {
			t.Fatal("concurrent history lost exchange accounting")
		}
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
