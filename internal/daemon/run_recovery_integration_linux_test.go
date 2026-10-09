//go:build linux

package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"example.org/resilient-proxy/internal/xray"
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
	for _, port := range []*int{&base.APIPort, &base.HTTPPort, &base.SocksPort, &base.TransparentPort, &base.ProbeBasePort, &base.DNSPolicyBasePort} {
		socket, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		*port = socket.Addr().(*net.TCPAddr).Port
		socket.Close()
	}
	root := t.TempDir()
	paths := Paths{RunRoot: filepath.Join(root, "run"), DataRoot: filepath.Join(root, "data"), Binary: binary}
	cfg := reloadFixture()
	cfg.Base = base
	cfg.RouteEnabled = false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
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
