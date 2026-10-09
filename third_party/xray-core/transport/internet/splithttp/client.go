package splithttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"

	"github.com/apernet/quic-go/http3"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/signal/done"
	"github.com/xtls/xray-core/transport/internet"
)

// interface to abstract between use of browser dialer, vs net/http
type DialerClient interface {
	IsClosed() bool

	// ctx, url, sessionId, body, uploadOnly
	OpenStream(context.Context, string, string, io.Reader, bool) (io.ReadCloser, net.Addr, net.Addr, error)

	// ctx, url, sessionId, seqStr, body, contentLength
	PostPacket(context.Context, string, string, string, buf.MultiBuffer) error
}

// implements splithttp.DialerClient in terms of direct network connections
type DefaultDialerClient struct {
	transportConfig *Config
	owner           *internet.MemoryStreamConfig
	client          *http.Client
	closed          atomic.Bool
	httpVersion     string
	// pool of net.Conn, created using dialUploadConn
	uploadRawMu    sync.Mutex
	uploadRawPool  []*H1Conn
	dialUploadConn func(ctxInner context.Context) (net.Conn, error)
}

func (c *DefaultDialerClient) IsClosed() bool {
	return c.closed.Load()
}

func (c *DefaultDialerClient) OpenStream(ctx context.Context, url string, sessionId string, body io.Reader, uploadOnly bool) (wrc io.ReadCloser, remoteAddr, localAddr net.Addr, err error) {
	// this is done when the TCP/UDP connection to the server was established,
	// and we can unblock the Dial function and print correct net addresses in
	// logs
	gotConn := done.New()
	dialErr := make(chan error, 1)
	var connOnce sync.Once
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(connInfo httptrace.GotConnInfo) {
			connOnce.Do(func() {
				remoteAddr = connInfo.Conn.RemoteAddr()
				localAddr = connInfo.Conn.LocalAddr()
				gotConn.Close()
			})
		},
	})

	method := "GET" // stream-down
	if body != nil {
		method = c.transportConfig.GetNormalizedUplinkHTTPMethod() // stream-up/one
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		errors.LogInfoInner(ctx, err, "failed to create HTTP request for "+url)
		return nil, nil, nil, err
	}
	c.transportConfig.FillStreamRequest(req, sessionId, "")

	responseBody := &WaitReadCloser{wait: done.New()}
	wrc = responseBody
	release := c.owner.AcquireDialer()
	go func() {
		defer release()
		resp, err := c.client.Do(req)
		if err != nil {
			if !uploadOnly { // stream-down is enough
				c.closed.Store(true)
				errors.LogInfoInner(ctx, err, "failed to "+method+" "+url)
			}
			dialErr <- err
			gotConn.Close()
			common.Close(body)
			responseBody.Close()
			return
		}
		if resp.StatusCode != 200 && !uploadOnly {
			errors.LogInfo(ctx, "unexpected status ", resp.StatusCode)
		}
		if resp.StatusCode != 200 || uploadOnly { // stream-up
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close() // if it is called immediately, the upload will be interrupted also
			common.Close(body)
			responseBody.Close()
			return
		}
		responseBody.Set(resp.Body)
	}()

	<-gotConn.Wait()
	select {
	case err = <-dialErr:
		return nil, nil, nil, err
	default:
	}
	return
}

func (c *DefaultDialerClient) PostPacket(ctx context.Context, url string, sessionId string, seqStr string, payload buf.MultiBuffer) error {
	method := c.transportConfig.GetNormalizedUplinkHTTPMethod()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	c.transportConfig.FillPacketRequest(req, sessionId, seqStr, payload)

	if c.httpVersion != "1.1" {
		resp, err := c.client.Do(req)
		if err != nil {
			c.closed.Store(true)
			return err
		}

		io.Copy(io.Discard, resp.Body)
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return errors.New("bad status code:", resp.Status)
		}
	} else {
		// stringify the entire HTTP/1.1 request so it can be
		// safely retried. if instead req.Write is called multiple
		// times, the body is already drained after the first
		// request
		requestBuff := new(bytes.Buffer)
		requestBuff.Grow(512 + int(req.ContentLength))
		common.Must(req.Write(requestBuff))

		var uploadConn *H1Conn
		var h1UploadConn *H1Conn

		for {
			uploadConn = c.takeUploadConn()
			newConnection := uploadConn == nil
			if newConnection {
				newConn, err := c.dialUploadConn(ctx)
				if err != nil {
					return err
				}
				h1UploadConn = NewH1Conn(newConn)
				uploadConn = h1UploadConn
			} else {
				h1UploadConn = uploadConn
			}
			activeConn := h1UploadConn
			canceled := make(chan struct{})
			stopCancel := context.AfterFunc(ctx, func() { activeConn.Close(); close(canceled) })
			finishCancel := func() {
				if !stopCancel() {
					<-canceled
				}
			}
			if !newConnection {

				// TODO: Replace 0 here with a config value later
				// Or add some other condition for optimization purposes
				if h1UploadConn.UnreadedResponsesCount > 0 {
					resp, err := http.ReadResponse(h1UploadConn.RespBufReader, req)
					if err != nil {
						finishCancel()
						c.closed.Store(true)
						h1UploadConn.Close()
						return fmt.Errorf("error while reading response: %s", err.Error())
					}
					io.Copy(io.Discard, resp.Body)
					defer resp.Body.Close()
					if resp.StatusCode != 200 {
						finishCancel()
						h1UploadConn.Close()
						return fmt.Errorf("got non-200 error response code: %d", resp.StatusCode)
					}
				}
			}

			_, err := h1UploadConn.Write(requestBuff.Bytes())
			finishCancel()
			if ctx.Err() != nil {
				h1UploadConn.Close()
				return ctx.Err()
			}
			// if the write failed, we try another connection from
			// the pool, until the write on a new connection fails.
			// failed writes to a pooled connection are normal when
			// the connection has been closed in the meantime.
			if err == nil {
				break
			}
			h1UploadConn.Close()
			if newConnection {
				return err
			}
		}

		c.putUploadConn(uploadConn)
	}

	return nil
}

func (c *DefaultDialerClient) takeUploadConn() *H1Conn {
	c.uploadRawMu.Lock()
	defer c.uploadRawMu.Unlock()
	if len(c.uploadRawPool) == 0 {
		return nil
	}
	last := len(c.uploadRawPool) - 1
	conn := c.uploadRawPool[last]
	c.uploadRawPool[last] = nil
	c.uploadRawPool = c.uploadRawPool[:last]
	return conn
}

func (c *DefaultDialerClient) putUploadConn(conn *H1Conn) {
	c.uploadRawMu.Lock()
	// Bound idle socket retention after a burst; in-flight requests are not
	// limited and never live in this pool.
	if !c.closed.Load() && len(c.uploadRawPool) < 16 {
		c.uploadRawPool = append(c.uploadRawPool, conn)
		c.uploadRawMu.Unlock()
		return
	}
	c.uploadRawMu.Unlock()
	conn.Close()
}

func (c *DefaultDialerClient) Close() error {
	c.uploadRawMu.Lock()
	c.closed.Store(true)
	pooled := c.uploadRawPool
	c.uploadRawPool = nil
	c.uploadRawMu.Unlock()
	for _, conn := range pooled {
		conn.Close()
	}
	transport := c.client.Transport
	if idle, ok := transport.(interface{ CloseIdleConnections() }); ok {
		idle.CloseIdleConnections()
	}
	if h3Transport, ok := transport.(*http3.Transport); ok {
		h3Transport.Close()
	}
	return nil
}

type WaitReadCloser struct {
	wait   *done.Instance
	reader atomic.Pointer[io.ReadCloser]
}

func (w *WaitReadCloser) Set(rc io.ReadCloser) {
	w.reader.Store(&rc)
	if w.wait.Done() {
		if p := w.reader.Swap(nil); p != nil {
			(*p).Close()
		}
	}
	w.wait.Close()
}

func (w *WaitReadCloser) Read(b []byte) (int, error) {
	rc := w.reader.Load()
	if rc == nil {
		<-w.wait.Wait()
		if rc = w.reader.Load(); rc == nil {
			return 0, io.ErrClosedPipe
		}
	}
	return (*rc).Read(b)
}

func (w *WaitReadCloser) Close() error {
	w.wait.Close()
	if p := w.reader.Swap(nil); p != nil {
		return (*p).Close()
	}
	return nil
}
