package agentport

import "syscall"

func init() {
	checkpointCPUNanos = func() int64 {
		var value syscall.Rusage
		if syscall.Getrusage(syscall.RUSAGE_SELF, &value) != nil {
			return 0
		}
		return value.Utime.Nano() + value.Stime.Nano()
	}
}
