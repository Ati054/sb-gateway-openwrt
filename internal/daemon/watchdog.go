package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
	"time"
)

const procdWatchdogSeconds = 90

type healthProgress struct {
	origin  time.Time
	elapsed atomic.Int64
	seen    atomic.Bool
}

func newHealthProgress() *healthProgress { return &healthProgress{origin: time.Now()} }
func (p *healthProgress) mark() {
	p.elapsed.Store(time.Since(p.origin).Nanoseconds())
	p.seen.Store(true)
}
func (p *healthProgress) fresh(now time.Duration) bool {
	age := now - time.Duration(p.elapsed.Load())
	return p.seen.Load() && age >= 0 && age <= 30*time.Second
}

func superviseCritical(ctx context.Context, cancel context.CancelCauseFunc, name string, run func(context.Context) error) {
	err := run(ctx)
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		err = fmt.Errorf("stopped unexpectedly")
	}
	cancel(fmt.Errorf("%s: %w", name, err))
}

// procd owns the expiry timer, outside this Go runtime. Remote HTTPS results do
// not renew or expire it: only controller progress and a responsive local core
// count. No third gateway runtime and no heartbeat writes to flash are needed.
func watchProcd(ctx context.Context, instance string, progress *healthProgress, coreReady func(context.Context) error, renew func(context.Context) error) {
	if instance != "gateway" {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	warned := false
	for {
		if ctx.Err() != nil {
			return
		}
		if progress.fresh(time.Since(progress.origin)) {
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := coreReady(callCtx)
			if err == nil {
				err = renew(callCtx)
			}
			cancel()
			if err != nil && ctx.Err() == nil {
				if !warned {
					log.Printf("resproxy: watchdog lease not renewed: %v", err)
				}
				warned = true
			} else if err == nil {
				warned = false
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func renewProcdWatchdog(ctx context.Context, instance string) error {
	payload, err := json.Marshal(map[string]any{"name": "resproxy", "instance": instance, "timeout": procdWatchdogSeconds})
	if err != nil {
		return err
	}
	body, err := boundedCommand(ctx, "ubus", "call", "service", "watchdog", string(payload)).Output()
	if err != nil {
		return err
	}
	var reply struct {
		Name     string `json:"name"`
		Instance string `json:"instance"`
		Timeout  int    `json:"timeout"`
		Mode     int    `json:"mode"`
	}
	if err = json.Unmarshal(body, &reply); err != nil {
		return err
	}
	if reply.Name != "resproxy" || reply.Instance != instance || reply.Timeout != procdWatchdogSeconds || reply.Mode != 1 {
		return fmt.Errorf("unexpected procd watchdog acknowledgement")
	}
	return nil
}
