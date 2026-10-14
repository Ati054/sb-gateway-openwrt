//go:build linux

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

func TestIndependentRecoveryWithRealXrayChildAndTraffic(t *testing.T) {
	binary := os.Getenv("RESPROXY_RECOVERY_BINARY")
	if binary == "" {
		t.Skip("set RESPROXY_RECOVERY_BINARY to the xray alias of the full gateway executable")
	}
	config := xray.DefaultBaseConfig()
	config.ProbeBudget = 1
	for _, port := range []*int{&config.APIPort, &config.TransparentPort, &config.HTTPPort, &config.SocksPort, &config.ProbeBasePort, &config.DNSPolicyBasePort} {
		socket, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		*port = socket.Addr().(*net.TCPAddr).Port
		socket.Close()
	}
	// Rendered background listeners occupy consecutive probe ports.
	if config.ProbeBasePort > 65000 {
		config.ProbeBasePort = 42000
	}
	body, err := xray.RenderBase(config)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "xray.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	server := fmt.Sprintf("127.0.0.1:%d", config.APIPort)
	engine, err := startCore("process", body, path, binary, server)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := newSupervisedCore(engine)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := xray.NewManager(binary, server, root)
	manager.Run = supervisor.Command
	if err = waitAPI(ctx, manager, "resproxy-main", engine.exited, 20*time.Second); err != nil {
		supervisor.Close()
		t.Fatal(err)
	}
	definition := json.RawMessage(`{"protocol":"freedom","settings":{}}`)
	if _, err = manager.Switch(ctx, "resproxy-main", "local-origin", definition); err != nil {
		supervisor.Close()
		t.Fatal(err)
	}
	var localIP net.IP
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if ip, ok := address.(*net.IPNet); ok && ip.IP.To4() != nil && ip.IP.IsPrivate() && !ip.IP.IsLoopback() {
			localIP = ip.IP
			break
		}
	}
	if localIP == nil {
		t.Fatal("private non-loopback address required by protected routing")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(localIP.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "recovered-child-traffic") }))
	origin.Listener = listener
	origin.Start()
	defer origin.Close()
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", config.HTTPPort))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	verify := func() error {
		response, err := client.Get(origin.URL)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		if string(body) != "recovered-child-traffic" {
			return fmt.Errorf("unexpected traffic: %q", body)
		}
		return nil
	}
	if err = verify(); err != nil {
		supervisor.Close()
		t.Fatal(err)
	}
	readyPath := filepath.Join(root, "ready")
	os.WriteFile(readyPath, []byte(fmt.Sprint(engine.pid)), 0600)
	done := make(chan error, 1)
	go func() {
		// Allow software-emulated ARM startup overhead in this transport test.
		// Production allowance and crash-loop deadlines are covered separately.
		doneErr := supervisor.run(ctx, 90*time.Second, func(ctx context.Context) error { _, err := manager.Current(ctx, "resproxy-main"); return err }, func() error { return os.Remove(readyPath) }, func(recovery context.Context) (*runningCore, error) {
			replacement, err := startCore("process", body, path, binary, server)
			if err != nil {
				return nil, err
			}
			local := xray.NewManager(binary, server, root)
			local.Run = replacement.Command
			if err = waitAPI(recovery, local, "resproxy-main", replacement.exited, 20*time.Second); err == nil {
				err = initializeCoreSelectors(recovery, local, []string{"resproxy-main"})
			}
			if err == nil {
				_, err = local.Switch(recovery, "resproxy-main", "local-origin", definition)
			}
			if err == nil {
				err = os.WriteFile(readyPath, []byte(fmt.Sprint(replacement.pid)), 0600)
			}
			if err != nil {
				replacement.Close()
				return nil, err
			}
			return replacement, nil
		})
		done <- doneErr
	}()
	defer func() { cancel(); <-done; supervisor.Close() }()
	controllerPID := os.Getpid()
	for _, signal := range []syscall.Signal{syscall.SIGKILL, syscall.SIGSTOP} {
		supervisor.mu.RLock()
		old := supervisor.current.pid
		supervisor.mu.RUnlock()
		started := time.Now()
		if err = syscall.Kill(old, signal); err != nil {
			t.Fatal(err)
		}
		var newPID int
		for time.Since(started) < 60*time.Second {
			supervisor.mu.RLock()
			newPID = supervisor.current.pid
			ready := supervisor.ready
			supervisor.mu.RUnlock()
			if ready && newPID != old && verify() == nil {
				break
			}
			select {
			case err := <-done:
				done <- err
				t.Fatalf("controller escalated prematurely: %v", err)
			default:
			}
			time.Sleep(50 * time.Millisecond)
		}
		if newPID == old || os.Getpid() != controllerPID {
			t.Fatal("core was not recovered independently")
		}
		if err = verify(); err != nil {
			t.Fatal(err)
		}
		if err = syscall.Kill(old, 0); err != syscall.ESRCH {
			t.Fatalf("old child not reaped: %v", err)
		}
		t.Logf("signal=%s recovered core %d -> %d, controller=%d, elapsed=%s", signal, old, newPID, controllerPID, time.Since(started))
	}
}
