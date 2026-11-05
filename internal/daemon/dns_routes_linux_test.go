//go:build linux

package daemon

import (
	"os"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/dnsroute"
)

func TestCoreRecoveryWithdrawsDNSAndRejectsOldObserver(t *testing.T) {
	identity, err := dnsroute.ProcessCore(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	routes := dnsroute.New([]string{"resproxy-main"})
	routes.SetCore(identity)
	core := &runningCore{pid: os.Getpid(), close: func() error { return nil }}
	supervisor := newSupervisedCore(core)
	supervisor.routes, supervisor.routeCore = routes, identity
	old := supervisor.routeObserver()
	old("resproxy-main", "a")
	read := func() (s dnsroute.Stamp) {
		routes.Read("resproxy-main", func(stamp dnsroute.Stamp) { s = stamp })
		return
	}
	if read().Tag != "a" {
		t.Fatal("initial readback not published")
	}
	supervisor.withdraw(time.Now().Add(time.Second))
	old("resproxy-main", "a")
	if read().Tag != "" {
		t.Fatal("old callback revived withdrawn core")
	}
	if !supervisor.admit(core) {
		t.Fatal("same-process new core incarnation not admitted")
	}
	current := supervisor.routeObserver()
	current("resproxy-main", "b")
	old("resproxy-main", "a")
	if read().Tag != "b" {
		t.Fatal("late old callback overwrote replacement")
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	current("resproxy-main", "b")
	if read().Tag != "" {
		t.Fatal("closed core retained DNS authorization")
	}
}
