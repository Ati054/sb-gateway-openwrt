package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"example.org/resilient-proxy/internal/settings"
)

// OpenWrt's date uses /etc/TZ, including custom POSIX zones. Go's default
// location does not interpret that file. Cache only the current offset in RAM;
// re-read it every 30 seconds to follow timezone and daylight-saving changes.
type routerClock struct {
	checked  time.Time
	location *time.Location
	read     func(context.Context) ([]byte, error)
}

func (clock *routerClock) now(ctx context.Context, now time.Time) (time.Time, error) {
	if clock.location != nil && now.Sub(clock.checked) >= 0 && now.Sub(clock.checked) < 30*time.Second {
		return now.In(clock.location), nil
	}
	read := clock.read
	if read == nil {
		read = func(ctx context.Context) ([]byte, error) {
			return exec.CommandContext(ctx, "/bin/date", "+%Y-%m-%d %H:%M:%S %z").Output()
		}
	}
	limited, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	output, err := read(limited)
	if err != nil {
		clock.location = nil
		return time.Time{}, fmt.Errorf("read router clock: %w", err)
	}
	local, err := time.Parse("2006-01-02 15:04:05 -0700", strings.TrimSpace(string(output)))
	if err != nil {
		clock.location = nil
		return time.Time{}, fmt.Errorf("parse router clock: %w", err)
	}
	clock.checked, clock.location = now, local.Location()
	return now.In(clock.location), nil
}

type dailyRefreshSchedule map[string]string

func (schedule dailyRefreshSchedule) due(source settings.Subscription, now time.Time) bool {
	day := now.Format("2006-01-02")
	atOrAfter := now.Format("15:04") >= source.UpdateTime
	previous, initialized := schedule[source.ID]
	if !initialized {
		// Start at the next occurrence, rather than refreshing on every restart.
		schedule[source.ID] = ""
		if atOrAfter {
			schedule[source.ID] = day
		}
		return false
	}
	return atOrAfter && day > previous
}

func (schedule dailyRefreshSchedule) attempted(source settings.Subscription, now time.Time) {
	if now.Format("15:04") >= source.UpdateTime {
		schedule[source.ID] = now.Format("2006-01-02")
	}
}
