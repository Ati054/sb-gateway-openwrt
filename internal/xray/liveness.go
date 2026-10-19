package xray

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// CheckLiveness distinguishes a busy control transport from a stopped core.
// A complete SOCKS method negotiation proves proxy-loop progress without
// contacting a remote server. Configuration/readback still requires the API.
func (m *Manager) CheckLiveness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.LivenessSOCKS == "" {
		return m.CheckAPI(ctx)
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- m.CheckAPI(check) }()
	delay := time.Second
	if deadline, ok := check.Deadline(); ok && time.Until(deadline)/2 < delay {
		delay = time.Until(deadline) / 2
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	started := false
	startSOCKS := func() {
		if started {
			return
		}
		started = true
		go func() { results <- checkSOCKSGreeting(check, m.LivenessSOCKS) }()
	}
	var failures []error
	for {
		select {
		case err := <-results:
			if err == nil {
				return nil
			}
			failures = append(failures, err)
			if len(failures) == 2 {
				return errors.Join(failures...)
			}
			startSOCKS()
		case <-timer.C:
			startSOCKS()
		case <-check.Done():
			return errors.Join(append(failures, check.Err())...)
		}
	}
}

func checkSOCKSGreeting(ctx context.Context, address string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("proxy liveness connect: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if n, writeErr := conn.Write([]byte{5, 1, 0}); writeErr != nil {
		return fmt.Errorf("proxy liveness write: %w", writeErr)
	} else if n != 3 {
		return io.ErrShortWrite
	}
	var reply [2]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil {
		return fmt.Errorf("proxy liveness reply: %w", err)
	}
	if reply != [2]byte{5, 0} {
		return errors.New("proxy liveness invalid SOCKS negotiation")
	}
	return nil
}
