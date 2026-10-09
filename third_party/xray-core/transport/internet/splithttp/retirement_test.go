package splithttp

import (
	"context"
	"io"
	gonet "net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol/tls/cert"
	"github.com/xtls/xray-core/testing/servers/tcp"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/internet/tls"
)

func TestXHTTPRetirementKeepsLiveStreamsAndReclaimsCache(t *testing.T) {
	for _, mode := range []string{"packet-up", "stream-up", "stream-one"} {
		t.Run(mode, func(t *testing.T) {
			certificate, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
			serverCertificate := tls.ParseCertificate(certificate)
			serverCertificate.OneTimeLoading = true
			serverSettings := &internet.MemoryStreamConfig{
				ProtocolName: "splithttp", ProtocolSettings: &Config{Path: "retirement"},
				SecurityType: "tls", SecuritySettings: &tls.Config{Certificate: []*tls.Certificate{serverCertificate}},
			}
			port := tcp.PickPort()
			listener, err := ListenXH(context.Background(), net.LocalHostIP, port, serverSettings,
				func(conn stat.Connection) { go func() { defer conn.Close(); io.Copy(conn, conn) }() })
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			dest := net.TCPDestination(net.LocalHostIP, port)
			for generation := 0; generation < 12; generation++ {
				settings := &internet.MemoryStreamConfig{
					ProtocolName: "splithttp", ProtocolSettings: &Config{Path: "retirement", Mode: mode,
						Xmux: &XmuxConfig{HMaxRequestTimes: &RangeConfig{From: 2, To: 2}}},
					SecurityType: "tls", SecuritySettings: &tls.Config{PinnedPeerCertSha256: [][]byte{hash[:]}},
				}
				first, err := Dial(context.Background(), dest, settings)
				if err != nil {
					t.Fatal(err)
				}
				second, err := Dial(context.Background(), dest, settings)
				if err != nil {
					first.Close()
					t.Fatal(err)
				}
				settings.RetireDialers()
				xhttpRetirementEcho(t, first)
				first.Close()
				// packet-up crosses its request limit after retirement and must
				// keep the replacement upload client in the same drain lifecycle.
				for range 4 {
					xhttpRetirementEcho(t, second)
				}
				second.Close()
				second.Close()
				waitXHTTPRetired(t, dest, settings)
				late, err := Dial(context.Background(), dest, settings)
				if err != nil {
					t.Fatal(err)
				}
				xhttpRetirementEcho(t, late)
				late.Close()
				waitXHTTPRetired(t, dest, settings)
			}
		})
	}
}

func TestXHTTPRetirementBoundsSilentTLSSetup(t *testing.T) {
	previous := xhttpHandshakeTimeout
	xhttpHandshakeTimeout = 100 * time.Millisecond
	defer func() { xhttpHandshakeTimeout = previous }()
	for _, fingerprint := range []string{"", "chrome"} {
		t.Run("fingerprint="+fingerprint, func(t *testing.T) {
			listener, err := gonet.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan gonet.Conn, 1)
			go func() {
				conn, err := listener.Accept()
				if err == nil {
					accepted <- conn
				}
			}()
			port := net.Port(listener.Addr().(*gonet.TCPAddr).Port)
			dest := net.TCPDestination(net.LocalHostIP, port)
			settings := &internet.MemoryStreamConfig{
				ProtocolName: "splithttp", ProtocolSettings: &Config{Mode: "stream-one"},
				SecurityType: "tls", SecuritySettings: &tls.Config{ServerName: "localhost", Fingerprint: fingerprint},
			}
			result := make(chan error, 1)
			go func() {
				conn, err := Dial(context.Background(), dest, settings)
				if conn != nil {
					conn.Close()
				}
				result <- err
			}()
			var peer gonet.Conn
			select {
			case peer = <-accepted:
				defer peer.Close()
			case <-time.After(time.Second):
				t.Fatal("silent TLS peer was not reached")
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("silent TLS establishment returned a live stream")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("silent TLS establishment did not time out")
			}
			settings.RetireDialers()
			waitXHTTPRetired(t, dest, settings)
		})
	}
}

func xhttpRetirementEcho(t *testing.T, conn stat.Connection) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte("live"))
		if err == nil {
			var response [4]byte
			_, err = io.ReadFull(conn, response[:])
			if err == nil && string(response[:]) != "live" {
				err = io.ErrUnexpectedEOF
			}
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		conn.Close()
		t.Fatal("live XHTTP stream stopped after retirement")
	}
}

func waitXHTTPRetired(t *testing.T, dest net.Destination, settings *internet.MemoryStreamConfig) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		globalDialerAccess.Lock()
		_, remains := globalDialerMap[dialerConf{dest, settings}]
		globalDialerAccess.Unlock()
		if !remains {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("retired XHTTP generation remains cached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestXHTTPRetirementIncludesDownloadSettings(t *testing.T) {
	owner := &internet.MemoryStreamConfig{ProtocolSettings: &Config{}}
	download := &internet.MemoryStreamConfig{ProtocolSettings: &Config{}}
	dest := net.TCPDestination(net.LocalHostIP, 443)
	release := owner.AcquireDialer()
	first, _ := getHTTPClient(context.Background(), dest, owner, owner)
	second, _ := getHTTPClient(context.Background(), dest, download, owner)
	owner.RetireDialers()
	if first.IsClosed() || second.IsClosed() {
		t.Fatal("closed clients with a live lease")
	}
	release()
	if !first.IsClosed() || !second.IsClosed() {
		t.Fatal("retained upload/download clients")
	}
	waitXHTTPRetired(t, dest, owner)
	waitXHTTPRetired(t, dest, download)
}

type retirementRoundTripper struct{ idleClosed bool }

func (*retirementRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { panic("unused") }
func (r *retirementRoundTripper) CloseIdleConnections()                         { r.idleClosed = true }

func TestXHTTPRetirementClosesIdleTransportsAndRawSockets(t *testing.T) {
	transport := &retirementRoundTripper{}
	client := &DefaultDialerClient{client: &http.Client{Transport: transport}}
	first, peer := gonet.Pipe()
	defer peer.Close()
	client.putUploadConn(NewH1Conn(first))
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if !transport.idleClosed || len(client.uploadRawPool) != 0 {
		t.Fatal("idle resources retained")
	}
	peer.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := peer.Write([]byte("x")); err == nil {
		t.Fatal("raw idle socket remains open")
	}
	late, latePeer := gonet.Pipe()
	defer latePeer.Close()
	client.putUploadConn(NewH1Conn(late))
	latePeer.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := latePeer.Write([]byte("x")); err == nil {
		t.Fatal("late idle socket re-entered closed pool")
	}
}

func TestXHTTPRetirementBoundsIdleRawSockets(t *testing.T) {
	client := &DefaultDialerClient{client: &http.Client{Transport: &retirementRoundTripper{}}}
	defer client.Close()
	for i := 0; i < 32; i++ {
		conn, peer := gonet.Pipe()
		defer peer.Close()
		client.putUploadConn(NewH1Conn(conn))
		if i >= 16 {
			peer.SetWriteDeadline(time.Now().Add(time.Second))
			if _, err := peer.Write([]byte("x")); err != io.ErrClosedPipe {
				t.Fatalf("excess idle socket retained: %v", err)
			}
		}
	}
	if len(client.uploadRawPool) != 16 {
		t.Fatalf("unexpected idle pool size: %d", len(client.uploadRawPool))
	}
}

func TestXHTTPRetirementCancelsPendingRequestsOnlyAfterStreamClose(t *testing.T) {
	for _, mode := range []string{"packet-up", "stream-up", "stream-one"} {
		t.Run(mode, func(t *testing.T) {
			var active atomic.Int32
			started := make(chan struct{}, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				active.Add(1)
				defer active.Add(-1)
				started <- struct{}{}
				io.Copy(io.Discard, r.Body)
				<-r.Context().Done()
			}))
			defer server.Close()
			defer server.CloseClientConnections()
			dest := net.TCPDestination(net.LocalHostIP, net.Port(server.Listener.Addr().(*gonet.TCPAddr).Port))
			settings := &internet.MemoryStreamConfig{ProtocolName: "splithttp", ProtocolSettings: &Config{Mode: mode}}
			conn, err := Dial(context.Background(), dest, settings)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("request did not reach the silent server")
			}
			settings.RetireDialers()
			if active.Load() == 0 {
				t.Fatal("retirement canceled a live request")
			}
			conn.Close()
			waitXHTTPRetired(t, dest, settings)
			deadline := time.Now().Add(3 * time.Second)
			for active.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if active.Load() != 0 {
				t.Fatal("closed stream left HTTP requests running")
			}
		})
	}
}

func TestXHTTPRetirementClosingHungStreamPreservesLiveSibling(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer silent.Close()
	defer silent.CloseClientConnections()
	silentDest := net.TCPDestination(net.LocalHostIP, net.Port(silent.Listener.Addr().(*gonet.TCPAddr).Port))
	port := tcp.PickPort()
	liveDest := net.TCPDestination(net.LocalHostIP, port)
	settings := &internet.MemoryStreamConfig{ProtocolName: "splithttp", ProtocolSettings: &Config{Mode: "packet-up"}}
	listener, err := ListenXH(context.Background(), net.LocalHostIP, port,
		&internet.MemoryStreamConfig{ProtocolName: "splithttp", ProtocolSettings: &Config{}},
		func(conn stat.Connection) { go func() { defer conn.Close(); io.Copy(conn, conn) }() })
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	live, err := Dial(context.Background(), liveDest, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	xhttpRetirementEcho(t, live)
	hung, err := Dial(context.Background(), silentDest, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer hung.Close()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("silent request did not start")
	}
	settings.RetireDialers()
	hung.Close()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("hung request was not canceled by its own Close")
	}
	xhttpRetirementEcho(t, live)
	live.Close()
	waitXHTTPRetired(t, liveDest, settings)
	waitXHTTPRetired(t, silentDest, settings)
}
