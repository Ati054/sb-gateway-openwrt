//go:build linux

package dnsproxy

import (
	"bytes"
	"net"
	"os"
	"testing"
)

func TestUDPQueueIsBoundedWithoutChangingGlobalLimit(t *testing.T) {
	before, err := os.ReadFile("/proc/sys/net/core/rmem_max")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, budget := range []int64{16 << 10, 4 << 20, 1 << 40} {
		actual := configureUDPQueue(conn, budget)
		if actual <= 0 || actual > 2<<20 {
			t.Fatalf("invalid kernel memory allowance: %d", actual)
		}
	}
	after, err := os.ReadFile("/proc/sys/net/core/rmem_max")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("DNS changed the system receive-buffer limit")
	}
}
