package outbound

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/session"
)

func TestSBServerFailureSignalIsLocalAndMinimal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unixgram is validated on the Linux target")
	}
	path := filepath.Join(t.TempDir(), "failures.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("SB_XRAY_FAILURE_SOCKET", path)
	signalSBServerFailure(context.Background(), "selected-tag", "dial")
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var buffer [513]byte
	count, _, err := listener.ReadFromUnix(buffer[:])
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	if err := json.Unmarshal(buffer[:count], &message); err != nil {
		t.Fatal(err)
	}
	if len(message) != 4 || message["v"] != float64(1) || message["pid"] != float64(os.Getpid()) || message["tag"] != "selected-tag" || message["stage"] != "dial" {
		t.Fatalf("unexpected failure signal: %v", message)
	}
	signalSBServerFailure(context.Background(), "selected-tag", "dial")
	if err := listener.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listener.ReadFromUnix(buffer[:]); err == nil {
		t.Fatal("repeated traffic failure bypassed the rate limit")
	}
	probe := session.ContextWithInbound(context.Background(), &session.Inbound{Tag: "outbound-health-probe"})
	signalSBServerFailure(probe, "probe-only-tag", "dial")
	if err := listener.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listener.ReadFromUnix(buffer[:]); err == nil {
		t.Fatal("background probe created a traffic failure signal")
	}
}

func TestSBServerFailureSignalNeverBlocksWithoutListener(t *testing.T) {
	t.Setenv("SB_XRAY_FAILURE_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	start := time.Now()
	signalSBServerFailure(context.Background(), "selected-tag", "dial")
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("missing listener delayed traffic by %s", elapsed)
	}
}
