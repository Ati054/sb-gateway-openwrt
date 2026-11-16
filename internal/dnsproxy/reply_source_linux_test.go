//go:build linux

package dnsproxy

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// Linux routes 127/8 locally, allowing a multi-address regression test without
// privileges or changes to the host's interfaces. Connected UDP rejects a
// response from 127.0.0.1 when the query was sent to 127.0.0.2.
func TestUDPReplyKeepsQueriedLocalAddress(t *testing.T) {
	config, release := preparedFixture(t)
	prepared, err := Prepare(config)
	if err != nil {
		release()
		t.Fatal(err)
	}
	release()
	stop, _, err := prepared.Start(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, host := range []string{"127.0.0.2", "127.0.0.3", "127.0.0.1"} {
		client, err := net.Dial("udp4", net.JoinHostPort(host, strconv.Itoa(config.Port)))
		if err != nil {
			t.Fatal(err)
		}
		client.SetDeadline(time.Now().Add(time.Second))
		query := dnsQuery("local.test")
		// A malformed question gets an immediate FORMERR without external DNS.
		query[5] = 2
		if _, err := client.Write(query); err != nil {
			client.Close()
			t.Fatal(err)
		}
		var reply [512]byte
		n, err := client.Read(reply[:])
		client.Close()
		if err != nil {
			t.Fatalf("reply from queried local address %s: %v", host, err)
		}
		if n != 12 || reply[3]&15 != 1 {
			t.Fatalf("unexpected response from %s: %x", host, reply[:n])
		}
	}
}
