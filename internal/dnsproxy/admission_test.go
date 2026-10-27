package dnsproxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/resources"
)

func listenerFixture(t *testing.T, delay time.Duration, workBytes int64) (*server, string, func()) {
	t.Helper()
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(cacheableDoHReply(query, 42))
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	client := newDoHClient(transport)
	t.Cleanup(client.CloseIdleConnections)
	// Select the shared port using TCP first: Windows may reserve a TCP range
	// while still handing the same numbers out as UDP ephemeral ports.
	var tcp net.Listener
	var udp net.PacketConn
	var err error
	for attempt := 0; attempt < 100; attempt++ {
		tcp, err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		udp, err = net.ListenPacket("udp4", tcp.Addr().String())
		if err == nil {
			break
		}
		tcp.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	configureUDPQueue(udp, workBytes)
	s := &server{config: Config{DefaultPolicy: "main", DirectURL: upstream.URL + "/dns-query", ProxyURL: upstream.URL + "/dns-query"},
		direct: client, proxy: client, cacheBudget: dnsCacheBudget(256 * resources.MiB),
		requestBudget: workBytes, clientMemory: newMemoryGate(dnsWorkBudget(256*resources.MiB), dnsClientMemory)}
	ctx, cancel := context.WithCancel(context.Background())
	s.workers.Add(2)
	go func() { defer s.workers.Done(); s.serveUDP(ctx, udp) }()
	go func() { defer s.workers.Done(); s.serveTCP(ctx, tcp) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			udp.Close()
			tcp.Close()
			s.requests().close()
			s.workers.Wait()
			upstream.CloseClientConnections()
			upstream.Close()
		})
	}
	t.Cleanup(stop)
	return s, udp.LocalAddr().String(), stop
}

func wireQuery(network, address, name string) error {
	conn, err := net.DialTimeout(network, address, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	query := dnsQuery(name)
	if _, err := conn.Write(query); err != nil {
		return err
	}
	response := make([]byte, 4096)
	n, err := conn.Read(response)
	if err != nil {
		return err
	}
	if n < 12 || response[3]&15 != 0 || !matchingQuestion(query, response[:n]) {
		return fmt.Errorf("invalid DNS response for %s (size=%d rcode=%d matching=%v)", name, n, response[3]&15, matchingQuestion(query, response[:n]))
	}
	return nil
}

func TestIdleTCPClientsCannotStarveUDP(t *testing.T) {
	s, address, _ := listenerFixture(t, 0, dnsWorkBudget(256*resources.MiB))
	var idle []net.Conn
	defer func() {
		for _, c := range idle {
			c.Close()
		}
	}()
	for i := 0; i < 128; i++ {
		c, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		idle = append(idle, c)
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.tcpClients.Load() < int64(len(idle)) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.tcpClients.Load() != int64(len(idle)) {
		t.Fatal("idle clients were rejected")
	}
	if err := wireQuery("udp", address, "udp-survives.test"); err != nil {
		t.Fatal(err)
	}
}

func TestBurstQueuesWithinRAMBudget(t *testing.T) {
	for _, scenario := range []struct {
		clients int
		work    int64
	}{{256, dnsWorkBudget(256 * resources.MiB)}, {12, 2 * dnsRequestMemory}} {
		t.Run(fmt.Sprint(scenario.clients), func(t *testing.T) {
			_, address, _ := listenerFixture(t, 100*time.Millisecond, scenario.work)
			start := make(chan struct{})
			errors := make(chan error, scenario.clients)
			var wg sync.WaitGroup
			for i := 0; i < scenario.clients; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					errors <- wireQuery("udp", address, fmt.Sprintf("cold-%d.test", i))
				}(i)
			}
			close(start)
			wg.Wait()
			close(errors)
			for err := range errors {
				if err != nil {
					t.Error(err)
				}
			}
		})
	}
}

func TestPersistentTCPHasNoSixteenQueryCutoff(t *testing.T) {
	_, address, _ := listenerFixture(t, 0, dnsWorkBudget(256*resources.MiB))
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i := 0; i < 64; i++ {
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		query := dnsQuery(fmt.Sprintf("persistent-%d.test", i))
		packet := binary.BigEndian.AppendUint16(nil, uint16(len(query)))
		packet = append(packet, query...)
		if _, err := conn.Write(packet); err != nil {
			t.Fatal(i, err)
		}
		var prefix [2]byte
		if _, err := io.ReadFull(conn, prefix[:]); err != nil {
			t.Fatal(i, err)
		}
		response := make([]byte, int(binary.BigEndian.Uint16(prefix[:])))
		if _, err := io.ReadFull(conn, response); err != nil {
			t.Fatal(i, err)
		}
		if len(response) < 12 || response[3]&15 != 0 || !matchingQuestion(query, response) {
			t.Fatal(i, "bad TCP DNS reply")
		}
	}
}

func TestBudgetsScaleWithRouterMemory(t *testing.T) {
	for _, mib := range []int64{256, 512, 1024} {
		ram := mib * resources.MiB
		if dnsWorkBudget(ram) != ram/64 || dnsCacheBudget(ram) != ram/128 {
			t.Fatal("DNS budget did not scale", mib)
		}
	}
}
