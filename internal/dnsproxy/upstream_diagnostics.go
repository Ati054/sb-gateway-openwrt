package dnsproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/http2"
)

var errDNSUpstreamClosed = errors.New("DNS upstream is closed")
var errDNSResponseMismatch = errors.New("DNS response does not match question")

// These diagnostics describe failed upstream exchanges, not their recipients:
// one coalesced exchange may fail several client requests. Never retain the
// original error text, endpoint, queried name, policy name or client address.
var upstreamFailureNames = [...]string{"timeout", "canceled", "eof", "reset", "refused", "broken_pipe", "unreachable", "tls", "http_response", "response_mismatch", "closed", "http2", "other"}

type upstreamFailureSample struct {
	At        string `json:"at"`
	Cause     string `json:"cause"`
	Protocol  string `json:"protocol"`
	Phase     string `json:"phase,omitempty"`
	Attempts  int    `json:"attempts,omitempty"`
	Reused    bool   `json:"connection_reused"`
	Status    int    `json:"http_status,omitempty"`
	HTTP2Code string `json:"http2_code,omitempty"`
}

type upstreamFailurePath struct {
	counts [len(upstreamFailureNames)]uint64
	last   upstreamFailureSample
}

type upstreamFailureDiagnostics struct {
	mu        sync.Mutex
	paths     [2]upstreamFailurePath
	sequence  uint64
	written   uint64
	published bool
}

// Attach only fixed metadata to the returned error. Unwrap preserves existing
// timeout classification and the bounded retry rules.
type upstreamExchangeError struct {
	cause    error
	protocol string
	phase    int
	attempts int
	reused   bool
}

func (e *upstreamExchangeError) Error() string { return e.cause.Error() }
func (e *upstreamExchangeError) Unwrap() error { return e.cause }

func upstreamFailureCause(err error) int {
	var n net.Error
	var response upstreamResponseError
	var verification *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var stream http2.StreamError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &n) && n.Timeout()):
		return 0
	case errors.Is(err, context.Canceled):
		return 1
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return 2
	case errors.Is(err, syscall.ECONNRESET):
		return 3
	case errors.Is(err, syscall.ECONNREFUSED):
		return 4
	case errors.Is(err, syscall.EPIPE):
		return 5
	case errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH):
		return 6
	case errors.As(err, &verification) || errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &certificate) || errors.As(err, &record):
		return 7
	case errors.As(err, &response):
		return 8
	case errors.Is(err, errDNSResponseMismatch):
		return 9
	case errors.Is(err, errDNSUpstreamClosed) || errors.Is(err, net.ErrClosed):
		return 10
	case errors.As(err, &stream):
		return 11
	}
	// net/http's bundled HTTP/2 errors have unexported types. Classify known
	// prefixes without retaining any part of their potentially private text.
	message := err.Error()
	if strings.Contains(message, "http2:") || strings.Contains(message, "stream error: stream ID") {
		return 11
	}
	if strings.Contains(message, "tls:") {
		return 7
	}
	return 12
}

func (d *upstreamFailureDiagnostics) record(viaWAN bool, protocol string, err error) {
	if err == nil {
		return
	}
	cause := upstreamFailureCause(err)
	sample := upstreamFailureSample{At: time.Now().UTC().Format(time.RFC3339Nano), Cause: upstreamFailureNames[cause]}
	switch protocol {
	case "doh", "dot", "tcp", "udp":
		sample.Protocol = protocol
	}
	var exchange *upstreamExchangeError
	if errors.As(err, &exchange) {
		if exchange.protocol == "tcp" || exchange.protocol == "udp" {
			sample.Protocol = exchange.protocol
		}
		if exchange.phase >= 0 && exchange.phase < 5 {
			sample.Phase = [...]string{"connect", "tls", "write", "headers", "body"}[exchange.phase]
		}
		sample.Attempts, sample.Reused = exchange.attempts, exchange.reused
	}
	var response upstreamResponseError
	if errors.As(err, &response) {
		sample.Status = response.status
	}
	var stream http2.StreamError
	if errors.As(err, &stream) {
		if stream.Code <= http2.ErrCodeHTTP11Required {
			sample.HTTP2Code = stream.Code.String()
		} else {
			sample.HTTP2Code = "unknown"
		}
	}
	index := 0
	if viaWAN {
		index = 1
	}
	d.mu.Lock()
	d.paths[index].counts[cause]++
	d.paths[index].last = sample
	d.sequence++
	d.mu.Unlock()
}

func (s *server) upstreamErrorsPath() string {
	return filepath.Join(filepath.Dir(s.config.StatusPath), "dns-upstream-errors.json")
}

func (s *server) writeUpstreamErrors() error {
	if s.config.StatusPath == "" {
		return nil
	}
	d := &s.upstreamErrors
	d.mu.Lock()
	if d.published && d.written == d.sequence {
		d.mu.Unlock()
		return nil
	}
	sequence, paths := d.sequence, d.paths
	d.mu.Unlock()
	type pathReport struct {
		Failed uint64                 `json:"failed_exchanges"`
		Causes map[string]uint64      `json:"causes"`
		Last   *upstreamFailureSample `json:"last,omitempty"`
	}
	report := make(map[string]pathReport, 2)
	for index, name := range []string{"proxy", "wan"} {
		path := pathReport{Causes: map[string]uint64{}}
		for cause, count := range paths[index].counts {
			if count != 0 {
				path.Failed += count
				path.Causes[upstreamFailureNames[cause]] = count
			}
		}
		if path.Failed != 0 {
			path.Last = &paths[index].last
		}
		report[name] = path
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.config.StatusPath), ".dns-upstream-errors-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), s.upstreamErrorsPath()); err != nil {
		return err
	}
	d.mu.Lock()
	d.written, d.published = sequence, true
	d.mu.Unlock()
	return nil
}
