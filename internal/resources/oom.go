package resources

import (
	"os"
	"strconv"
	"strings"
)

// Kernel OOM records include a monotonic timestamp. A SIGKILL alone is not
// evidence of OOM; require the exact PID and a record from this core lifetime.
func oomRecord(record string, pid int, since float64) bool {
	header, message, ok := strings.Cut(record, ";")
	if !ok {
		return false
	}
	fields := strings.Split(header, ",")
	if len(fields) < 3 {
		return false
	}
	micros, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil || float64(micros)/1e6 < since {
		return false
	}
	return strings.Contains(message, "Killed process "+strconv.Itoa(pid)+" (")
}
func BootSeconds() float64 {
	body, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	values := strings.Fields(string(body))
	if len(values) == 0 {
		return 0
	}
	seconds, _ := strconv.ParseFloat(values[0], 64)
	return seconds
}
