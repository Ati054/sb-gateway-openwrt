package dnsproxy

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func preparedFixture(t *testing.T) (Config, func()) {
	t.Helper()
	for attempt := 0; attempt < 10; attempt++ {
		udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := udp.LocalAddr().(*net.UDPAddr).Port
		tcp, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			udp.Close()
			continue
		}
		root := t.TempDir()
		return Config{Port: port, ProxyPort: 19080, DefaultPolicy: "main", Mode: "proxy-only",
			ProxyURL: "https://1.1.1.1/dns-query", DirectURL: "https://1.1.1.1/dns-query",
			StatusPath: filepath.Join(root, "status.json"), GenerationPath: filepath.Join(root, "generation.json"),
			Policies: []Policy{{ID: "main", DirectDomains: []string{"direct.test"}}}}, func() { udp.Close(); tcp.Close() }
	}
	t.Fatal("could not reserve a shared UDP/TCP port")
	return Config{}, nil
}

func TestPreparedDNSDefersSocketsAndPublication(t *testing.T) {
	config, release := preparedFixture(t)
	defer release()
	prepared, err := Prepare(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{config.StatusPath, config.GenerationPath, filepath.Join(filepath.Dir(config.StatusPath), "dns-rejected.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preparation published runtime state: %s: %v", path, err)
		}
	}
	if !prepared.server.matchesDirect("main", "sub.direct.test") || prepared.server.matchesDirect("main", "other.test") {
		t.Fatal("preparation changed domain routing")
	}
	release()
	stop, update, err := prepared.Start(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := os.Stat(config.GenerationPath); err != nil {
		t.Fatal("started DNS did not publish its generation", err)
	}
	if _, _, err := prepared.Start(t.Context()); err == nil {
		t.Fatal("preparation started twice")
	}
	if err := update([]Policy{{ID: "main", DirectDomains: []string{"new.test"}}}); err != nil {
		t.Fatal(err)
	}
	if !prepared.server.matchesDirect("main", "new.test") || prepared.server.matchesDirect("main", "direct.test") {
		t.Fatal("prepared listener lost hot policy replacement")
	}
	stop()
	if _, err := os.Stat(filepath.Join(filepath.Dir(config.StatusPath), "dns-rejected.json")); !os.IsNotExist(err) {
		t.Fatal("stop retained rejected-packet diagnostics")
	}
	udp, err := net.ListenPacket("udp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(config.Port)))
	if err != nil {
		t.Fatal("stop retained UDP socket", err)
	}
	udp.Close()
	tcp, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(config.Port)))
	if err != nil {
		t.Fatal("stop retained TCP socket", err)
	}
	tcp.Close()
}

func TestPreparedDNSCancellationDoesNotBindOrConsume(t *testing.T) {
	config, release := preparedFixture(t)
	defer release()
	prepared, err := Prepare(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := prepared.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation bound sockets: %v", err)
	}
	if _, err := os.Stat(config.GenerationPath); !os.IsNotExist(err) {
		t.Fatal("canceled preparation published runtime state")
	}
	release()
	stop, _, err := prepared.Start(t.Context())
	if err != nil {
		t.Fatal("cancellation consumed preparation", err)
	}
	stop()
}
