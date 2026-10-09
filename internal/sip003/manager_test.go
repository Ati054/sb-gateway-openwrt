package sip003

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestPluginHelper(t *testing.T) {
	if os.Getenv("RESPROXY_TEST_PLUGIN_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(os.Getenv("SS_LOCAL_HOST"), os.Getenv("SS_LOCAL_PORT")))
	if err != nil {
		os.Exit(2)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			os.Exit(3)
		}
		go func() { defer conn.Close(); io.Copy(conn, conn) }()
	}
}
func TestLazySharingRestartAndRelease(t *testing.T) {
	t.Setenv("RESPROXY_TEST_PLUGIN_HELPER", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	defer manager.Close()
	manager.BinaryPath = func(string) string { return binary }
	definition := func(tag string) map[string]any {
		var out map[string]any
		json.Unmarshal([]byte(`{"protocol":"shadowsocks","settings":{"address":"remote.example","port":8388,"password":"private"},"_resproxy_plugin":{"name":"v2ray-plugin","options":""}}`), &out)
		out["tag"] = tag
		return out
	}
	first := definition("one")
	if err := manager.Prepare(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := definition("two")
	if err := manager.Prepare(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if len(manager.entries) != 1 || first[Metadata] != nil || first["settings"].(map[string]any)["port"] != second["settings"].(map[string]any)["port"] {
		t.Fatal("helper not shared / metadata leaked")
	}
	port := first["settings"].(map[string]any)["port"].(int)
	var helper *helper
	for _, h := range manager.entries {
		helper = h
	}
	oldPID := helper.cmd.Process.Pid
	helper.cmd.Process.Kill()
	time.Sleep(50 * time.Millisecond)
	if err := manager.Prepare(context.Background(), definition("one")); err != nil {
		t.Fatal(err)
	}
	if helper.cmd.Process.Pid == oldPID || helper.port != port {
		t.Fatal("crashed helper not restored on original port")
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	conn.Write([]byte("echo"))
	b := make([]byte, 4)
	if _, err = io.ReadFull(conn, b); err != nil || string(b) != "echo" {
		t.Fatalf("local tunnel: %v", err)
	}
	conn.Close()
	time.Sleep(50 * time.Millisecond)
	manager.Release("one")
	manager.Release("two")
	helper.last = time.Now().Add(-time.Minute)
	manager.Sweep(context.Background(), map[string]bool{})
	if helper.cmd != nil {
		t.Fatal("idle helper still running")
	}
	if err := manager.Prepare(context.Background(), definition("three")); err != nil {
		t.Fatal(err)
	}
	if helper.port != port {
		t.Fatal("cached handler port changed")
	}
}
