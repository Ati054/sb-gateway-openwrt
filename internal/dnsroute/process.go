package dnsroute

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func ProcessCore(pid int) (Core, error) {
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Core{}, err
	}
	return parseCore(pid, string(body))
}

func parseCore(pid int, stat string) (Core, error) {
	end := strings.LastIndex(stat, ")")
	if end < 0 {
		return Core{}, fmt.Errorf("invalid core process identity")
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return Core{}, fmt.Errorf("missing core process start time")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return Core{}, fmt.Errorf("invalid core process start time")
	}
	return Core{PID: pid, StartTicks: fields[19]}, nil
}
