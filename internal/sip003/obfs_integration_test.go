package sip003

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Exercise the installed upstream HTTP-obfs client, including actual SIP003
// environment handling and bidirectional transport. No test server is shipped.
func TestInstalledObfsHTTPTransport(t *testing.T) {
	if os.Getenv("RESPROXY_TEST_PLUGINS") != "1" {
		t.Skip("installed router plugin required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	payload := "sip003-transport"
	seen := make(chan string, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(4 * time.Second))
				reader := bufio.NewReader(conn)
				var header strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					header.WriteString(line)
					if line == "\r\n" {
						break
					}
					if header.Len() > 16384 {
						return
					}
				}
				body := make([]byte, len(payload))
				if _, err := io.ReadFull(reader, body); err != nil {
					return
				}
				seen <- header.String()
				io.WriteString(conn, "HTTP/1.1 200 OK\r\nServer: fixture\r\n\r\n"+string(body))
			}()
		}
	}()
	manager := NewManager()
	defer manager.Close()
	outbound := map[string]any{"protocol": "shadowsocks", "tag": "obfs-test", "settings": map[string]any{"address": "127.0.0.1", "port": listener.Addr().(*net.TCPAddr).Port, "password": "never-passed-to-plugin"}, Metadata: map[string]any{"name": "obfs-local", "options": "obfs=http;obfs-host=front.example"}}
	if err := manager.Prepare(context.Background(), outbound); err != nil {
		t.Fatal(err)
	}
	local := outbound["settings"].(map[string]any)["port"].(int)
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(local))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(4 * time.Second))
	fmt.Fprint(conn, payload)
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil || string(response) != payload {
		t.Fatalf("HTTP obfs round trip: %v", err)
	}
	select {
	case header := <-seen:
		if !strings.Contains(header, "front.example") {
			t.Fatal("obfs host option lost")
		}
	case <-time.After(time.Second):
		t.Fatal("no obfs request captured")
	}
}
