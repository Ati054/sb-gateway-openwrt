//go:build linux

package daemon

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

// Exercise the whole daemon event loop, which must not compete with the
// supervisor for the child's closed exit channel.
func TestRunKeepsControllerAliveAcrossRealCoreFailure(t *testing.T) {
	binary := os.Getenv("RESPROXY_RECOVERY_BINARY")
	if binary == "" {
		t.Skip("set RESPROXY_RECOVERY_BINARY to the full gateway's xray alias")
	}
	base := xray.DefaultBaseConfig()
	base.ProbeBudget = 1
	releasePorts := reserveCoreRecoveryPorts(t, &base)
	t.Cleanup(releasePorts)
	root := t.TempDir()
	paths := Paths{RunRoot: filepath.Join(root, "run"), DataRoot: filepath.Join(root, "data"), Binary: binary}
	cfg := reloadFixture()
	cfg.Base = base
	cfg.RouteEnabled = false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	releasePorts()
	go func() { done <- Run(ctx, cfg, paths) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("daemon did not stop")
		}
	}()
	marker := filepath.Join(paths.RunRoot, "xray-ready")
	awaitCore := func(old int) int {
		deadline := time.Now().Add(75 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case err := <-done:
				done <- err
				t.Fatalf("controller exited during core recovery: %v", err)
			default:
			}
			data, _ := os.ReadFile(marker)
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 1 && pid != old {
				return pid
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("core readiness was not restored")
		return 0
	}
	pid := awaitCore(0)
	for _, signal := range []syscall.Signal{syscall.SIGKILL, syscall.SIGSTOP} {
		t.Logf("failure signal=%v core=%d", signal, pid)
		if err := syscall.Kill(pid, signal); err != nil {
			t.Fatal(err)
		}
		next := awaitCore(pid)
		manager := xray.NewManager(binary, fmt.Sprintf("127.0.0.1:%d", base.APIPort), paths.RunRoot)
		if _, err := manager.Current(ctx, "resproxy-"+base.DefaultPolicy); err != nil {
			t.Fatalf("restored API unavailable: %v", err)
		}
		if err := syscall.Kill(pid, 0); err == nil {
			t.Fatal("previous child survived recovery")
		}
		pid = next
	}
}

// Keep the complete TCP/UDP block leased until handoff. Listen(:0) chooses
// ephemeral ports which the fixture's own API clients can claim after release.
func reserveCoreRecoveryPorts(t *testing.T, base *xray.BaseConfig) func() {
	t.Helper()
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		t.Fatal(err)
	}
	var low, high int
	if n, err := fmt.Sscanf(string(raw), "%d %d", &low, &high); err != nil || n != 2 || low < 1 || high > 65535 || low > high {
		t.Fatal("invalid ephemeral port range")
	}
	count := 4 + base.ProbeLaneCount() + 1 + len(base.Policies)
	for attempt := 0; attempt < 500; attempt++ {
		first := 1024 + rand.IntN(65536-1024-count)
		if first <= high && first+count-1 >= low {
			continue
		}
		var leases []io.Closer
		release := func() {
			for _, socket := range leases {
				socket.Close()
			}
		}
		failed := false
		for offset := 0; offset < count && !failed; offset++ {
			port := first + offset
			for _, network := range []string{"tcp4", "udp4", "tcp6", "udp6"} {
				host := "127.0.0.1"
				if strings.HasSuffix(network, "6") {
					host = "::1"
				}
				address := net.JoinHostPort(host, strconv.Itoa(port))
				var socket io.Closer
				var err error
				if strings.HasPrefix(network, "tcp") {
					socket, err = net.Listen(network, address)
				} else {
					socket, err = net.ListenPacket(network, address)
				}
				if err != nil {
					failed = true
					break
				}
				leases = append(leases, socket)
			}
		}
		if failed {
			release()
			continue
		}
		base.APIPort = first
		base.HTTPPort = first + 1
		base.SocksPort = first + 2
		base.TransparentPort = first + 3
		base.ProbeBasePort = first + 4
		base.DNSPolicyBasePort = base.ProbeBasePort + base.ProbeLaneCount() + 1
		return release
	}
	t.Fatal("could not reserve a complete recovery fixture port block outside ephemeral ports")
	return nil
}

func TestCoreRecoveryPortLeaseCoversRefreshAndBothProtocols(t *testing.T) {
	base := xray.DefaultBaseConfig()
	base.ProbeBudget = 1
	release := reserveCoreRecoveryPorts(t, &base)
	defer release()
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for port := base.APIPort; port < base.DNSPolicyBasePort+len(base.Policies); port++ {
		for _, network := range []string{"tcp4", "udp4", "tcp6", "udp6"} {
			host := "127.0.0.1"
			if strings.HasSuffix(network, "6") {
				host = "::1"
			}
			address := net.JoinHostPort(host, strconv.Itoa(port))
			var socket io.Closer
			var err error
			if strings.HasPrefix(network, "tcp") {
				socket, err = net.Listen(network, address)
			} else {
				socket, err = net.ListenPacket(network, address)
			}
			if err == nil {
				socket.Close()
				t.Fatalf("fixture port %d/%s was not leased", port, network)
			}
		}
	}
}
