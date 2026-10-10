package agentport

import (
	"os"
	"strconv"
	"strings"
)

type cpuCounters struct{ total, idle uint64 }

// Read kernel counters only during an existing probe, without a sampler or
// retained history. Guest fields already belong to user/nice and must not be
// added twice. Missing counters leave the overload evidence unknown.
func readProbeCPU(path string) (cpuCounters, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return cpuCounters{}, false
	}
	fields := strings.Fields(strings.SplitN(string(body), "\n", 2)[0])
	if len(fields) < 9 || fields[0] != "cpu" {
		return cpuCounters{}, false
	}
	var c cpuCounters
	for i := 1; i <= 8; i++ {
		v, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return cpuCounters{}, false
		}
		c.total += v
		if i == 4 || i == 5 {
			c.idle += v
		}
	}
	return c, true
}
func probeCPUSaturated(before, after cpuCounters) bool {
	if after.total <= before.total || after.idle < before.idle {
		return false
	}
	total, idle := after.total-before.total, after.idle-before.idle
	return idle <= total && idle*100 <= total*5
}

func unconfirmedCPUTimeout(evidence probeEvidence) bool {
	return !evidence.OK && evidence.CPUOverloaded && evidence.Failure == probeFailureTimeout
}
