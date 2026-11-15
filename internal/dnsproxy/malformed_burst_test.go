package dnsproxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type observedNoiseConn struct {
	net.PacketConn
	received *atomic.Uint64
}

func (c *observedNoiseConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, peer, err := c.PacketConn.ReadFrom(p)
	if err == nil && (n == 1280 || n == 1284) {
		c.received.Add(1)
	}
	return n, peer, err
}

// Synthetic packets reproduce only the reported numeric header shapes. They
// are not a replay of customer traffic or an authenticated QUIC connection.
func TestMalformedBurstCannotStarveValidWANAndProxyDNS(t *testing.T) {
	for _, burst := range []int{200, 1000} {
		t.Run(fmt.Sprint(burst), func(t *testing.T) {
			var receivedNoise atomic.Uint64
			s, address, _ := listenerFixtureObserved(t, 30*time.Millisecond, 8*dnsRequestMemory, func(conn net.PacketConn) net.PacketConn {
				return &observedNoiseConn{PacketConn: conn, received: &receivedNoise}
			})
			noise, err := net.Dial("udp4", address)
			if err != nil {
				t.Fatal(err)
			}
			defer noise.Close()
			noise.SetDeadline(time.Now().Add(5 * time.Second))
			quicLike := make([]byte, 1280)
			quicLike[0], quicLike[4], quicLike[5], quicLike[14] = 0xc3, 1, 8, 8
			responseLike := make([]byte, 1284)
			for i := range responseLike {
				responseLike[i] = 0x5a
			}
			responseLike[2] |= 0x80
			start := make(chan struct{})
			errors := make(chan error, 34)
			var group sync.WaitGroup
			group.Go(func() {
				<-start
				for i := range burst {
					p := quicLike
					if i%2 != 0 {
						p = responseLike
					}
					if _, err := noise.Write(p); err != nil {
						errors <- err
						return
					}
					if i%10 == 9 {
						time.Sleep(5 * time.Millisecond)
					}
				}
			})
			group.Go(func() {
				<-start
				conn, err := net.DialTimeout("tcp4", address, 5*time.Second)
				if err != nil {
					errors <- err
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				p := dnsQuery("synthetic-noise.example.test")
				p[2], p[5] = 2, 2
				frame := binary.BigEndian.AppendUint16(nil, uint16(len(p)))
				frame = append(frame, p...)
				for range 32 {
					if _, err := conn.Write(frame); err != nil {
						errors <- err
						return
					}
					var reply [14]byte
					if _, err := io.ReadFull(conn, reply[:]); err != nil {
						errors <- err
						return
					}
					if binary.BigEndian.Uint16(reply[:2]) != 12 || reply[5]&15 != 1 {
						errors <- fmt.Errorf("malformed TCP query did not receive FORMERR")
						return
					}
				}
			})
			for i := range 16 {
				group.Go(func() {
					<-start
					for n := range 2 {
						name := fmt.Sprintf("valid-%d-%d.example.test", i, n)
						if i%2 == 0 {
							if err := wireQuery("udp", address, name); err != nil {
								errors <- err
								return
							}
						} else {
							ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
							q := dnsQuery(name)
							peer := netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)}), uint16(40000+i))
							r := s.resolvePeer(ctx, q, peer, ingressUDP)
							cancel()
							if len(r) < 12 || r[3]&15 != 0 || !matchingQuestion(q, r) {
								errors <- fmt.Errorf("valid proxy DNS failed under malformed burst")
								return
							}
						}
					}
				})
			}
			close(start)
			group.Wait()
			close(errors)
			for err := range errors {
				t.Error(err)
			}
			// UDP can be dropped by the kernel under pressure. Count independent
			// successful socket reads rather than equating writes with delivery.
			// A final query on the same flow fences all retained noise datagrams,
			// and must itself still succeed through the WAN resolver.
			noise.SetDeadline(time.Now().Add(5 * time.Second))
			fence := dnsQuery("after-noise.example.test")
			if _, err := noise.Write(fence); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 4096)
			for {
				n, err := noise.Read(buffer)
				if err != nil {
					t.Fatalf("valid DNS after burst failed: %v", err)
				}
				if matchingQuestion(fence, buffer[:n]) {
					if n < 12 || buffer[3]&15 != 0 {
						t.Fatal("post-burst DNS was rejected")
					}
					break
				}
			}
			delivered := receivedNoise.Load()
			if delivered == 0 || delivered > uint64(burst) {
				t.Fatalf("invalid noise observer: %d/%d", delivered, burst)
			}
			wantInvalid := delivered + 32
			deadline := time.Now().Add(5 * time.Second)
			if s.failureInvalidQuery.Load() != wantInvalid || s.failures.Load() != wantInvalid || s.queries.Load() != wantInvalid+33 {
				t.Fatalf("unexpected accounting: queries=%d invalid=%d failures=%d", s.queries.Load(), s.failureInvalidQuery.Load(), s.failures.Load())
			}
			if s.upstreamExchanges.Load() != 33 || s.directUse.Load() != 17 || s.proxyUse.Load() != 16 || s.fallbackUse.Load() != 0 {
				t.Fatal("malformed traffic reached upstream or changed valid DNS routes")
			}
			for {
				stats := s.requests().stats()
				if stats["dns_admission_rejected"] != 0 || stats["dns_queue_timeouts"] != 0 {
					t.Fatalf("valid DNS lost admission under malformed input: %v", stats)
				}
				if stats["active_upstream"] == 0 && stats["queued_upstream"] == 0 && stats["dns_work_bytes"] == 0 && stats["pending_query_bytes"] == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("DNS work did not drain: %v", stats)
				}
				time.Sleep(time.Millisecond)
			}
			t.Logf("%d/%d UDP + 32 TCP malformed inputs received and rejected; 17 WAN + 16 proxy lookups succeeded; queue errors=0", delivered, burst)
		})
	}
}
