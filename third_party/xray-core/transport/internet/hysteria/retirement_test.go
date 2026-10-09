package hysteria

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol/tls/cert"
	"github.com/xtls/xray-core/proxy/hysteria/account"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/internet/tls"
)

func TestHysteriaRetirementKeepsTCPAndUDPAndReclaimsCache(t *testing.T) {
	certificate, hash := cert.MustGenerate(nil, cert.CommonName("localhost"))
	serverCertificate := tls.ParseCertificate(certificate)
	serverCertificate.OneTimeLoading = true
	listener, err := Listen(ContextWithValidator(context.Background(), account.NewValidator()), net.LocalHostIP, 0,
		&internet.MemoryStreamConfig{ProtocolName: "hysteria", ProtocolSettings: &Config{Auth: "retirement-test", UdpIdleTimeout: 60},
			SecurityType: "tls", SecuritySettings: &tls.Config{Certificate: []*tls.Certificate{serverCertificate}}},
		func(conn stat.Connection) { go func() { defer conn.Close(); io.Copy(conn, conn) }() })
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dest := net.UDPDestination(net.LocalHostIP, net.Port(listener.Addr().(*net.UDPAddr).Port))
	for generation := 0; generation < 24; generation++ {
		settings := &internet.MemoryStreamConfig{ProtocolName: "hysteria", ProtocolSettings: &Config{Auth: "retirement-test"},
			SecurityType: "tls", SecuritySettings: &tls.Config{PinnedPeerCertSha256: [][]byte{hash[:]}},
			QuicParams: &internet.QuicParams{DisableChromeParrot: true}}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		first, err := Dial(ctx, dest, settings)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		second, err := Dial(ContextWithDatagram(ctx, true), dest, settings)
		if err != nil {
			first.Close()
			cancel()
			t.Fatal(err)
		}
		manager.RLock()
		cached := manager.m[dialerConf{dest, settings}]
		manager.RUnlock()
		settings.RetireDialers()
		hysteriaRetirementEcho(t, first, []byte("live"), false)
		first.Close()
		hysteriaRetirementEcho(t, second, []byte{0, 0, 0, 0, 'l', 'i', 'v', 'e'}, true)
		second.Close()
		second.Close()
		manager.RLock()
		_, remains := manager.m[dialerConf{dest, settings}]
		manager.RUnlock()
		if remains {
			t.Fatal("retired Hysteria generation remains cached")
		}
		cached.Lock()
		open := cached.conn != nil || cached.tr != nil || cached.pktConn != nil || cached.udpSM != nil
		cached.Unlock()
		if open {
			t.Fatal("retired QUIC resources remain open")
		}
		late, err := Dial(ctx, dest, settings)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		hysteriaRetirementEcho(t, late, []byte("live"), false)
		late.Close()
		cancel()
		manager.RLock()
		_, remains = manager.m[dialerConf{dest, settings}]
		manager.RUnlock()
		if remains {
			t.Fatal("late Hysteria stream left a cache entry")
		}
	}
	settings := &internet.MemoryStreamConfig{ProtocolName: "hysteria", ProtocolSettings: &Config{Auth: "wrong-auth"},
		SecurityType: "tls", SecuritySettings: &tls.Config{PinnedPeerCertSha256: [][]byte{hash[:]}},
		QuicParams: &internet.QuicParams{DisableChromeParrot: true}}
	settings.RetireDialers()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if conn, err := Dial(ctx, dest, settings); err == nil {
		conn.Close()
		t.Fatal("invalid authentication unexpectedly succeeded")
	}
	manager.RLock()
	_, remains := manager.m[dialerConf{dest, settings}]
	manager.RUnlock()
	if remains {
		t.Fatal("failed Hysteria dial left a cache entry")
	}
}

func hysteriaRetirementEcho(t *testing.T, conn stat.Connection, payload []byte, datagram bool) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		if err == nil {
			response := make([]byte, 4)
			if datagram {
				response = make([]byte, 8)
			}
			_, err = io.ReadFull(conn, response)
			if err == nil && string(response[len(response)-4:]) != "live" {
				err = io.ErrUnexpectedEOF
			}
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		conn.Close()
		t.Fatal("live Hysteria stream stopped after retirement")
	}
}
