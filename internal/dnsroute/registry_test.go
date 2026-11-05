package dnsroute

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func snapshot(r *Registry, id string) (s Stamp) { r.Read(id, func(value Stamp) { s = value }); return }

func TestRegistryABAAndStaleCorePublisher(t *testing.T) {
	r := New([]string{"resproxy-main", "resproxy-other"})
	a := Core{42, "100"}
	r.SetCore(a)
	publish := r.Publisher(a)
	publish("resproxy-main", "leaf-a")
	first := snapshot(r, "resproxy-main")
	publish("resproxy-main", "leaf-a")
	if snapshot(r, "resproxy-main") != first {
		t.Fatal("unchanged readback advanced epoch")
	}
	publish("resproxy-main", "leaf-b")
	publish("resproxy-main", "leaf-a")
	last := snapshot(r, "resproxy-main")
	if last.Tag != first.Tag || last.Epoch <= first.Epoch {
		t.Fatal("ABA reused epoch")
	}
	other := snapshot(r, "resproxy-other")
	publish("resproxy-probe", "probe")
	if snapshot(r, "resproxy-other") != other || snapshot(r, "resproxy-probe") != (Stamp{}) {
		t.Fatal("probe entered production registry")
	}
	r.SetCore(Core{})
	publish("resproxy-main", "leaf-a")
	if snapshot(r, "resproxy-main").Tag != "" {
		t.Fatal("withdrawn publisher revived route")
	}
	b := Core{42, "101"} // PID reused, different process start.
	r.SetCore(b)
	fresh := r.Publisher(b)
	fresh("resproxy-main", "leaf-b")
	publish("resproxy-main", "leaf-a")
	if snapshot(r, "resproxy-main").Tag != "leaf-b" {
		t.Fatal("old core overwrote new route")
	}
}

func TestRouteCleanupRunsOutsidePublicationLock(t *testing.T) {
	r := New([]string{"resproxy-main"})
	core := Core{42, "5"}
	r.SetCore(core)
	var cleanups int
	stop := r.Subscribe(func(_ string, _ Stamp) func() { return func() { r.Read("resproxy-main", func(Stamp) { cleanups++ }) } })
	defer stop()
	done := make(chan struct{})
	go func() { r.Publisher(core)("resproxy-main", "a"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("socket cleanup held route writer lock")
	}
	if cleanups != 2 {
		t.Fatal("idle cleanup was lost")
	}
}

func TestRegistryReadIsAtomicWithPublicationAndUnsubscribe(t *testing.T) {
	r := New([]string{"resproxy-main"})
	core := Core{1, "20"}
	r.SetCore(core)
	publish := r.Publisher(core)
	var calls int
	stop := r.Subscribe(func(string, Stamp) func() { calls++; return nil })
	publish("resproxy-main", "a")
	stop()
	before := calls
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go r.Read("resproxy-main", func(old Stamp) {
		close(entered)
		<-release
		if old.Tag != "a" {
			panic("unexpected route")
		}
	})
	<-entered
	var writers sync.WaitGroup
	writers.Add(1)
	go func() { defer writers.Done(); publish("resproxy-main", "b"); close(done) }()
	select {
	case <-done:
		t.Fatal("publication crossed cache read transaction")
	default:
	}
	close(release)
	writers.Wait()
	if calls != before || snapshot(r, "resproxy-main").Tag != "b" {
		t.Fatal("unsubscribe/publication failed")
	}
}

func TestParseCoreWithParenthesesAndSpaces(t *testing.T) {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[19] = "S", "12345"
	core, err := parseCore(7, "7 (a (name) with spaces) "+strings.Join(fields, " "))
	if err != nil || core != (Core{7, "12345"}) {
		t.Fatal(core, err)
	}
	for _, bad := range []string{"", "7 (x) S", fmt.Sprintf("7 (x) %s", strings.ReplaceAll(strings.Join(fields, " "), "12345", "bad"))} {
		if _, err := parseCore(7, bad); err == nil {
			t.Fatal("accepted malformed identity")
		}
	}
}
