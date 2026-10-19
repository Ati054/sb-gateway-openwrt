package dnsproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Each policy owns one current transport. Retired transports drain existing
// requests; completion closes their idle sockets without aborting healthy work.
type recoveringTransport struct {
	mu      sync.Mutex
	current *http.Transport
	closed  bool
}

// Public resolver sessions are bounded globally across policy transports.
// Connections still validate their configured hostname and certificate chain.
var dnsTLSClientSessions = tls.NewLRUClientSessionCache(8)

func newDoHClient(transport *http.Transport) *http.Client {
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	if transport.TLSClientConfig.ClientSessionCache == nil {
		transport.TLSClientConfig.ClientSessionCache = dnsTLSClientSessions
	}
	return &http.Client{Transport: &recoveringTransport{current: transport}, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("DoH redirect is not allowed") }}
}
func (r *recoveringTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r.mu.Lock()
	transport, closed := r.current, r.closed
	r.mu.Unlock()
	if closed {
		return nil, errors.New("DNS upstream is closed")
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		if timeoutError(err) && !errors.Is(request.Context().Err(), context.Canceled) {
			r.retire(transport)
		}
		r.closeRetired(transport)
		return nil, err
	}
	response.Body = &drainingBody{ReadCloser: response.Body, owner: r, transport: transport, ctx: request.Context()}
	return response, nil
}
func (r *recoveringTransport) retire(transport *http.Transport) {
	r.mu.Lock()
	if r.current == transport && !r.closed {
		r.current = transport.Clone()
	}
	r.mu.Unlock()
	transport.CloseIdleConnections()
}
func (r *recoveringTransport) closeRetired(transport *http.Transport) {
	r.mu.Lock()
	retired := r.current != transport || r.closed
	r.mu.Unlock()
	if retired {
		transport.CloseIdleConnections()
	}
}
func (r *recoveringTransport) CloseIdleConnections() {
	r.mu.Lock()
	r.closed = true
	transport := r.current
	r.mu.Unlock()
	transport.CloseIdleConnections()
}

type drainingBody struct {
	io.ReadCloser
	owner     *recoveringTransport
	transport *http.Transport
	ctx       context.Context
}

func (body *drainingBody) Read(p []byte) (int, error) {
	n, err := body.ReadCloser.Read(p)
	if err != nil && timeoutError(err) && !errors.Is(body.ctx.Err(), context.Canceled) {
		body.owner.retire(body.transport)
	}
	return n, err
}
func (body *drainingBody) Close() error {
	err := body.ReadCloser.Close()
	body.owner.closeRetired(body.transport)
	return err
}
func timeoutError(err error) bool { var n net.Error; return errors.As(err, &n) && n.Timeout() }
func retryDoH(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}

// Structured, bounded diagnostics never retain a client's queried domain or
// an upstream URL. They distinguish remote rejection from local saturation.
type upstreamResponseError struct{ status int }

func (e upstreamResponseError) Error() string { return "invalid DoH response" }

// upstreamTimeoutPhase keeps fixed-size diagnostics without retaining query names.
type upstreamTimeoutPhase struct {
	phase int
	cause error
}

func (e *upstreamTimeoutPhase) Error() string   { return e.cause.Error() }
func (e *upstreamTimeoutPhase) Unwrap() error   { return e.cause }
func (e *upstreamTimeoutPhase) Timeout() bool   { return true }
func (e *upstreamTimeoutPhase) Temporary() bool { return true }

func exchangeDoH(ctx context.Context, client *http.Client, endpoint string, query []byte) (result []byte, resultErr error) {
	var phase atomic.Int32
	defer func() {
		if resultErr != nil && (errors.Is(resultErr, context.DeadlineExceeded) || timeoutError(resultErr)) {
			resultErr = &upstreamTimeoutPhase{phase: int(phase.Load()), cause: resultErr}
		}
	}()
	trace := &httptrace.ClientTrace{
		GetConn:              func(string) { phase.Store(0) },
		TLSHandshakeStart:    func() { phase.Store(1) },
		GotConn:              func(httptrace.GotConnInfo) { phase.Store(2) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { phase.Store(3) },
		GotFirstResponseByte: func() { phase.Store(4) },
	}
	ctx = httptrace.WithClientTrace(ctx, trace)
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Do not mutate a client shared with other requests or permit redirects to
	// escape the configured resolver. Both attempts share one overall deadline.
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("DoH redirect is not allowed") }
	for attempt := 0; ; attempt++ {
		if err := requestCtx.Err(); err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(query))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/dns-message")
		request.Header.Set("Accept", "application/dns-message")
		response, err := bounded.Do(request)
		var body []byte
		if err == nil {
			mediaType, _, mimeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
			if response.StatusCode != http.StatusOK || mimeErr != nil || mediaType != "application/dns-message" {
				err = upstreamResponseError{status: response.StatusCode}
			} else {
				body, err = io.ReadAll(io.LimitReader(response.Body, maxDNSMessage+1))
				if err == nil && !matchingQuestion(query, body) {
					err = upstreamResponseError{status: response.StatusCode}
				}
			}
			_ = response.Body.Close()
		}
		if err == nil {
			return body, nil
		}
		if requestCtx.Err() != nil {
			return nil, requestCtx.Err()
		}
		if attempt != 0 || !retryDoH(err) {
			return nil, err
		}
	}
}

func matchingQuestion(query, response []byte) bool {
	if len(response) < 12 || len(response) > maxDNSMessage || len(query) < 12 ||
		!bytes.Equal(query[:2], response[:2]) || response[2]&0x80 == 0 ||
		response[2]&0x78 != query[2]&0x78 || response[4] != 0 || response[5] != 1 {
		return false
	}
	var q, a dnsmessage.Parser
	if _, err := q.Start(query); err != nil {
		return false
	}
	if _, err := a.Start(response); err != nil {
		return false
	}
	question, err := q.Question()
	if err != nil {
		return false
	}
	answer, err := a.Question()
	if err != nil {
		return false
	}
	return strings.EqualFold(question.Name.String(), answer.Name.String()) && question.Type == answer.Type && question.Class == answer.Class
}

func (s *server) publishGeneration(policies []Policy) error {
	if s.config.GenerationPath == "" {
		return nil
	}
	// Only DNS configuration changes advance this marker; periodic statistics
	// must never discard health proof. Startup gets a distinct RAM generation.
	if s.generationStarted == "" {
		s.generationStarted = fmt.Sprintf("%d:%d", os.Getpid(), time.Now().UnixNano())
	}
	body, err := json.Marshal(struct {
		Started                            string
		Proxy, Direct, Mode, DefaultPolicy string
		Ports                              map[string]int
		Policies                           []Policy
	}{s.generationStarted, s.config.ProxyURL, s.config.DirectURL, s.config.Mode, s.config.DefaultPolicy, s.config.PolicyProxyPorts, policies})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	body = []byte(fmt.Sprintf("%x\n", sum))
	if old, err := os.ReadFile(s.config.GenerationPath); err == nil && bytes.Equal(old, body) {
		return nil
	}
	file, err := os.CreateTemp(filepath.Dir(s.config.GenerationPath), ".dns-generation-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), s.config.GenerationPath)
}
