package resources

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Process struct {
	PID     int   `json:"pid"`
	RSSKiB  int64 `json:"rss_kib"`
	AnonKiB int64 `json:"anon_kib"`
	FDs     int   `json:"fds"`
	FDLimit int64 `json:"fd_limit"`
}
type Snapshot struct {
	UpdatedAt            time.Time  `json:"updated_at"`
	UpdatedUptimeSeconds float64    `json:"updated_uptime_seconds,omitempty"`
	TotalKiB             int64      `json:"total_kib"`
	AvailableKiB         int64      `json:"available_kib"`
	LowMemory            bool       `json:"low_memory"`
	Controller           *Process   `json:"controller"`
	Core                 *Process   `json:"core"`
	TCP                  *int64     `json:"tcp_sessions"`
	UDP                  *int64     `json:"udp_sessions"`
	Budget               Budget     `json:"budget"`
	ControllerGo         *GoRuntime `json:"controller_go"`
	LastOOM              *time.Time `json:"last_oom,omitempty"`
}

func readKiB(path string) map[string]int64 {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	values := map[string]int64{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && n >= 0 {
			values[strings.TrimSuffix(fields[0], ":")] = n
		}
	}
	return values
}
func ReadProcess(root string, pid int) *Process {
	if pid <= 0 {
		return nil
	}
	base := filepath.Join(root, strconv.Itoa(pid))
	values := readKiB(filepath.Join(base, "status"))
	if values == nil {
		return nil
	}
	result := &Process{PID: pid, RSSKiB: values["VmRSS"], AnonKiB: values["RssAnon"]}
	if fds, err := os.ReadDir(filepath.Join(base, "fd")); err == nil {
		result.FDs = len(fds)
	} else {
		result.FDs = -1
	}
	f, err := os.Open(filepath.Join(base, "limits"))
	if err == nil {
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			fields := strings.Fields(s.Text())
			if len(fields) >= 5 && strings.Join(fields[:3], " ") == "Max open files" {
				result.FDLimit, _ = strconv.ParseInt(fields[3], 10, 64)
				break
			}
		}
	}
	return result
}
func ReadSnapshot(corePID int) Snapshot {
	values := readKiB("/proc/meminfo")
	total, available := values["MemTotal"], values["MemAvailable"]
	threshold := total / 10
	if threshold < 16*1024 {
		threshold = 16 * 1024
	}
	return Snapshot{UpdatedAt: time.Now().UTC(), UpdatedUptimeSeconds: BootSeconds(), TotalKiB: total, AvailableKiB: available, LowMemory: values != nil && available < threshold, Controller: ReadProcess("/proc", os.Getpid()), Core: ReadProcess("/proc", corePID), Budget: CurrentBudget(), ControllerGo: ReadGoRuntime()}
}
