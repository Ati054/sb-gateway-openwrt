package dnsproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func newDoHClient(transport *http.Transport) *http.Client {
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

func exchangeDoH(ctx context.Context, client *http.Client, endpoint string, query []byte) ([]byte, error) {
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
				err = errors.New("invalid DoH response")
			} else {
				body, err = io.ReadAll(io.LimitReader(response.Body, maxDNSMessage+1))
				if err == nil && !matchingQuestion(query, body) {
					err = errors.New("DoH response does not match DNS question")
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
