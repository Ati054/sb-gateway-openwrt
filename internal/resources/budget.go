// Package resources budgets the two Go runtimes independently on small routers.
package resources

import (
	"bufio"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

const MiB int64 = 1 << 20

type Budget struct {
	Total     int64 `json:"total_bytes"`
	Daemon    int64 `json:"daemon_limit_bytes"`
	Core      int64 `json:"core_limit_bytes"`
	GCPercent int   `json:"gc_percent"`
}

// Reserve system headroom on supported router sizes for sockets, tmpfs and other
// services. These are soft Go runtime budgets, never advertised as RSS caps.
func ForMemory(total int64) Budget {
	if total <= 0 {
		total = 256 * MiB
	}
	daemon := total / 100 * 16
	core := total / 100 * 30
	if daemon < 24*MiB {
		daemon = 24 * MiB
	}
	if core < 32*MiB {
		core = 32 * MiB
	}
	gc := 100
	if total < 384*MiB {
		gc = 75
	}
	return Budget{Total: total, Daemon: daemon, Core: core, GCPercent: gc}
}

func readTotal(reader io.Reader) int64 {
	scanner := bufio.NewScanner(io.LimitReader(reader, 64<<10))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || fields[0] != "MemTotal:" || fields[2] != "kB" {
			continue
		}
		kib, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && kib > 0 && kib <= 1<<40 {
			return kib * 1024
		}
	}
	return 0
}

func CurrentBudget() Budget {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return ForMemory(0)
	}
	defer file.Close()
	return ForMemory(readTotal(file))
}

// Explicit administrator-supplied Go tuning remains authoritative. Setting a
// daemon limit through the runtime does not export it into the child's env.
func ConfigureDaemon() Budget {
	budget := CurrentBudget()
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(budget.Daemon)
	}
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(budget.GCPercent)
	}
	return budget
}

func CoreEnvironment(base []string, budget Budget) []string {
	env := append([]string(nil), base...)
	hasLimit, hasGC := false, false
	for _, value := range env {
		if strings.HasPrefix(value, "GOMEMLIMIT=") && strings.TrimPrefix(value, "GOMEMLIMIT=") != "" {
			hasLimit = true
		}
		if strings.HasPrefix(value, "GOGC=") && strings.TrimPrefix(value, "GOGC=") != "" {
			hasGC = true
		}
	}
	// Remove empty values too: duplicate environment keys are ambiguous to exec.
	clean := env[:0]
	for _, value := range env {
		if value != "GOMEMLIMIT=" && value != "GOGC=" {
			clean = append(clean, value)
		}
	}
	env = clean
	if !hasLimit {
		env = append(env, "GOMEMLIMIT="+strconv.FormatInt(budget.Core, 10))
	}
	if !hasGC {
		env = append(env, "GOGC="+strconv.Itoa(budget.GCPercent))
	}
	return env
}
