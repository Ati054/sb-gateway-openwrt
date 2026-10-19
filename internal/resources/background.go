package resources

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type backgroundGate struct {
	mu          sync.Mutex
	path        string
	now         func() time.Time
	updating    func() bool
	at          time.Time
	total, idle uint64
	known, busy bool
}

// NewBackgroundBusy defers optional quality scans at CPU saturation. Active
// path checks, startup restoration and emergency reserve discovery keep running.
func NewBackgroundBusy() func() bool {
	gate := &backgroundGate{path: "/proc/stat", now: time.Now, updating: UpdateInProgress}
	gate.read()
	return gate.read
}

func readCPU(path string) (total, idle uint64, ok bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(line)
	if len(fields) < 9 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i := 1; i <= 8; i++ {
		value, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return 0, 0, false
		}
		total += value
		if i == 4 || i == 5 {
			idle += value
		}
	}
	return total, idle, true
}

func (g *backgroundGate) read() bool {
	if g.updating != nil && g.updating() {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if g.known && now.Sub(g.at) >= 0 && now.Sub(g.at) < time.Second {
		return g.busy
	}
	total, idle, ok := readCPU(g.path)
	if !ok {
		g.known = false
		g.busy = false
		return false
	}
	g.busy = false
	if g.known && total > g.total && idle >= g.idle {
		// Match the 95% saturation threshold used to classify probe timeouts.
		g.busy = (idle-g.idle)*100 <= (total-g.total)*5
	}
	g.total, g.idle, g.at, g.known = total, idle, now, true
	return g.busy
}
