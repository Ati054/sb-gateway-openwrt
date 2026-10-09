package dnsproxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func dohReply(query []byte) []byte {
	reply := append([]byte(nil), query...)
	reply[2] |= 0x80
	return reply
}
func TestDoHBoundedRetryAndValidation(t *testing.T) {
	for _, scenario := range []string{"eof", "body-eof", "cancel", "deadline", "bad-mime", "wrong-name", "wrong-type", "wrong-class", "status", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			query := dnsQuery("example.com")
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			calls := 0
			client := &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 && scenario == "eof" {
					return nil, io.EOF
				}
				if scenario == "cancel" {
					cancel()
					return nil, io.EOF
				}
				if scenario == "deadline" {
					<-request.Context().Done()
					return nil, request.Context().Err()
				}
				body := dohReply(query)
				mime, status := "application/dns-message; charset=binary", 200
				switch scenario {
				case "bad-mime":
					mime = "application/dns-message-malicious"
				case "wrong-name":
					body = dohReply(dnsQuery("another.com"))
				case "wrong-type":
					body[len(body)-3] = 28
				case "wrong-class":
					body[len(body)-1] = 3
				case "status":
					status = 503
				case "oversized":
					body = append(body, make([]byte, maxDNSMessage)...)
				}
				response := &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{mime}}, Body: io.NopCloser(strings.NewReader(string(body)))}
				if scenario == "body-eof" && calls == 1 {
					response.Body = failingBody{}
				}
				return response, nil
			})}
			_, err := exchangeDoH(ctx, client, "https://resolver.test/dns-query", query)
			good := scenario == "eof" || scenario == "body-eof"
			if (err == nil) != good {
				t.Fatalf("result: %v", err)
			}
			want := 1
			if good {
				want = 2
			}
			if calls != want {
				t.Fatalf("attempts %d want %d", calls, want)
			}
		})
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingBody) Close() error             { return nil }

func TestDoHRedirectRefused(t *testing.T) {
	var escaped atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	if _, err := exchangeDoH(context.Background(), redirect.Client(), redirect.URL, dnsQuery("example.com")); err == nil {
		t.Fatal("redirect admitted")
	}
	if escaped.Load() != 0 {
		t.Fatal("request escaped resolver")
	}
}

func TestDoHRetirementKeepsInflightResponse(t *testing.T) {
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(dohReply(body))
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	client := newDoHClient(transport)
	owner := client.Transport.(*recoveringTransport)
	defer client.CloseIdleConnections()
	query := dnsQuery("example.com")
	request, _ := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader(string(query)))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	owner.retire(transport)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !matchingQuestion(query, body) {
		t.Fatalf("inflight lost: %v", err)
	}
	if _, err := exchangeDoH(context.Background(), client, upstream.URL, query); err != nil {
		t.Fatalf("replacement unusable: %v", err)
	}
	if owner.current == transport {
		t.Fatal("transport was not replaced")
	}
}

func TestWireCancellationInterruptsReadAndConnect(t *testing.T) {
	for _, protocol := range []string{"udp", "tcp", "connect"} {
		t.Run(protocol, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			received := make(chan struct{})
			var address string
			if protocol == "udp" {
				socket, err := net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer socket.Close()
				address = socket.LocalAddr().String()
				go func() {
					b := make([]byte, 4096)
					if _, _, err := socket.ReadFrom(b); err == nil {
						close(received)
					}
				}()
			} else {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				address = listener.Addr().String()
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					b := make([]byte, 4096)
					if _, err = conn.Read(b); err == nil {
						close(received)
					}
					<-ctx.Done()
				}()
			}
			done := make(chan error, 1)
			go func() {
				var err error
				if protocol == "connect" {
					_, port, _ := net.SplitHostPort(address)
					var value int
					for _, c := range port {
						value = value*10 + int(c-'0')
					}
					_, err = dialProxyConnect(ctx, value, "resolver.test:853")
				} else {
					_, err = exchangeWire(ctx, Endpoint{Protocol: protocol, Address: address}, dnsQuery("example.com"), true, 0)
				}
				done <- err
			}()
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("request not observed")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancel succeeded unexpectedly")
				}
			case <-time.After(300 * time.Millisecond):
				t.Fatal("cancel waited for timeout")
			}
		})
	}
}

func TestDNSStopJoinsSessionsAndPreservesGenerationOnFailedUpdate(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	path := filepath.Join(t.TempDir(), "dns-generation")
	config := Config{Port: port, ProxyPort: 19080, DefaultPolicy: "main", Mode: "proxy-only", ProxyURL: "https://1.1.1.1/dns-query", DirectURL: "https://1.1.1.1/dns-query", GenerationPath: path, Policies: []Policy{{ID: "main"}}}
	stop, update, err := Start(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := update([]Policy{{ID: "main", DirectServiceExpressions: []string{"regexp:["}}}); err == nil {
		t.Fatal("bad update admitted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed publication advanced generation")
	}
	if err := update(config.Policies); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("unchanged publication invalidated health")
	}
	if err := update([]Policy{{ID: "main", DirectDomains: []string{"example.com"}}}); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(path)
	if string(before) == string(after) {
		t.Fatal("DNS edit did not advance generation")
	}
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// An idle accepted TCP session must not keep Stop waiting for its ten-second read deadline.
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Stop did not drain workers")
	}
	stop2, _, err := Start(context.Background(), config)
	if err != nil {
		t.Fatalf("port not released: %v", err)
	}
	stop2()
}

func TestMatchingQuestionRejectsHeaderOnly(t *testing.T) {
	query := dnsQuery("example.com")
	reply := dohReply(query)
	binary.BigEndian.PutUint16(reply[4:6], 0)
	if matchingQuestion(query, reply) {
		t.Fatal("missing question accepted")
	}
}

func TestHTTP2TimeoutRetiresTransportWithoutAbortingHealthyStream(t *testing.T) {
	gate := make(chan struct{})
	var release sync.Once
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		if r.URL.Path == "/healthy" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		w.Write(dohReply(query))
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer func() { release.Do(func() { close(gate) }); upstream.Close() }()
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	client := newDoHClient(transport)
	defer client.CloseIdleConnections()
	query := dnsQuery("example.com")
	request, _ := http.NewRequest(http.MethodPost, upstream.URL+"/healthy", strings.NewReader(string(query)))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatal("HTTP/2 fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := exchangeDoH(ctx, client, upstream.URL+"/slow", query); err == nil {
		t.Fatal("stalled resolver did not time out")
	}
	owner := client.Transport.(*recoveringTransport)
	owner.mu.Lock()
	replaced := owner.current != transport
	owner.mu.Unlock()
	if !replaced {
		t.Fatal("timeout left stalled transport current")
	}
	if _, err := exchangeDoH(context.Background(), client, upstream.URL, query); err != nil {
		t.Fatalf("new transport failed: %v", err)
	}
	// Release the pre-existing response only after replacement has carried traffic.
	release.Do(func() { close(gate) })
	body, err := io.ReadAll(response.Body)
	if err != nil || !matchingQuestion(query, body) {
		t.Fatalf("healthy stream aborted on retirement: %v", err)
	}
}
