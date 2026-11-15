package dnsproxy

import (
	"context"
	"errors"
	"log"
	"net"
	"time"
)

func dnsListenConfig() net.ListenConfig {
	var config net.ListenConfig
	// Independent of GODEBUG=multipathtcp and the Go runtime default.
	config.SetMultipathTCP(false)
	return config
}

// A full descriptor table is temporary pressure, not a closed listener. Keep
// the existing socket and established clients; avoid both a tight error loop
// and a permanent loss of DNS/TCP after the pressure has passed.
func retryDNSAccept(ctx context.Context, err error, delay *time.Duration) bool {
	var temporary net.Error
	if ctx.Err() != nil || !errors.As(err, &temporary) || !temporary.Temporary() {
		return false
	}
	if *delay == 0 {
		*delay = 5 * time.Millisecond
		log.Printf("resproxy: temporary LAN DNS TCP accept failure; retrying: %v", err)
	} else {
		*delay = min(*delay*2, time.Second)
	}
	timer := time.NewTimer(*delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
