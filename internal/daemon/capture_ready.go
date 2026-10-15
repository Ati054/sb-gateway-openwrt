package daemon

import (
	"context"
	"log"
	"time"
)

// A failed firewall transaction retains the previous protection. Transient
// discovery/netlink errors must not tear down a healthy core and repeat its
// entire startup. Retry with bounded backoff while the supervisor and health
// workers remain active; shutdown must still cancel the wait immediately.
func waitInitialCapture(ctx context.Context, apply func(context.Context) error, delay time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := apply(ctx); err == nil {
			return nil
		} else {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("resproxy: LAN capture not ready; previous protection retained; retry in %s: %v", delay, err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}
