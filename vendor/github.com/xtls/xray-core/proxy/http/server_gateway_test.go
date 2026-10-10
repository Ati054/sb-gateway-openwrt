package http

import (
	"bufio"
	"context"
	"io"
	stdnet "net"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/transport"
)

type probePolicyManager struct{}

func (probePolicyManager) Type() interface{}              { return policy.ManagerType() }
func (probePolicyManager) Start() error                   { return nil }
func (probePolicyManager) Close() error                   { return nil }
func (probePolicyManager) ForLevel(uint32) policy.Session { return policy.SessionDefault() }
func (probePolicyManager) ForSystem() policy.System       { return policy.System{} }

type probeDispatcher struct{ got chan int }

func (d *probeDispatcher) Type() interface{} { return nil }
func (d *probeDispatcher) Start() error      { return nil }
func (d *probeDispatcher) Close() error      { return nil }
func (d *probeDispatcher) Dispatch(context.Context, net.Destination) (*transport.Link, error) {
	return nil, nil
}
func (d *probeDispatcher) DispatchLink(ctx context.Context, _ net.Destination, _ *transport.Link) error {
	d.got <- session.InboundFromContext(ctx).CanSpliceCopy
	return nil
}

// The real HTTP CONNECT path must disable splice only for bounded health
// requests. Splice extends the Vision inactivity timer to 24h, which can race
// the client's EOF and retain both the probe and upstream socket after close.
func TestGatewayHealthCONNECTKeepsOrdinaryEOFTimeouts(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want int
	}{
		{"outbound-health-probe", 3}, {"outbound-health-background", 3},
		{"outbound-health-background-10", 3}, {"resproxy-refresh-probe", 3},
		{"resproxy-http", 1}, {"external-http", 1}, {"outbound-healthiness", 1},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			listener, err := stdnet.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			client, err := stdnet.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			server, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			deadline := time.Now().Add(3 * time.Second)
			client.SetDeadline(deadline)
			server.SetDeadline(deadline)
			dispatcher := &probeDispatcher{got: make(chan int, 1)}
			ctx := session.ContextWithInbound(context.Background(), &session.Inbound{Tag: tc.tag})
			s := &Server{config: &ServerConfig{}, policyManager: probePolicyManager{}}
			finished := make(chan error, 1)
			go func() { finished <- s.Process(ctx, net.Network_TCP, server, dispatcher) }()
			_, err = io.WriteString(client, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
			if err != nil {
				t.Fatal(err)
			}
			status, err := bufio.NewReader(client).ReadString('\n')
			if err != nil || status != "HTTP/1.1 200 Connection established\r\n" {
				t.Fatalf("CONNECT: %q %v", status, err)
			}
			select {
			case got := <-dispatcher.got:
				if got != tc.want {
					t.Fatalf("splice flag %d, want %d", got, tc.want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("dispatch timeout")
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
		})
	}
}
