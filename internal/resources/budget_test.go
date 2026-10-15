package resources

import (
	"slices"
	"strings"
	"testing"
)

func TestBudgetsScaleWithRouterRAM(t *testing.T) {
	var previous Budget
	for _, mib := range []int64{128, 256, 512, 1024} {
		b := ForMemory(mib * MiB)
		if b.Daemon < 24*MiB || b.Core < 32*MiB || b.Daemon+b.Core >= b.Total/2 {
			t.Fatalf("unsafe budget for %d MiB: %+v", mib, b)
		}
		if b.Daemon <= previous.Daemon || b.Core <= previous.Core {
			t.Fatalf("budgets did not grow: %+v -> %+v", previous, b)
		}
		previous = b
	}
}
func TestMemoryParserDoesNotTrustInvalidUnitsOrOverflow(t *testing.T) {
	for _, raw := range []string{"MemTotal: -1 kB\n", "MemTotal: 1024 MB\n", "MemTotal: 999999999999999999999 kB\n"} {
		if got := readTotal(strings.NewReader(raw)); got != 0 {
			t.Fatal(raw, got)
		}
	}
	if got := readTotal(strings.NewReader("Other: 1 kB\nMemTotal: 262144 kB\n")); got != 256*MiB {
		t.Fatal(got)
	}
}
func TestCoreBudgetDoesNotMutateParentOrOverrideAdministrator(t *testing.T) {
	base := []string{"PATH=/bin", "GOMEMLIMIT=", "GOGC="}
	want := slices.Clone(base)
	env := CoreEnvironment(base, ForMemory(256*MiB))
	if !slices.Equal(base, want) || !slices.Contains(env, "GOGC=75") || slices.Contains(env, "GOMEMLIMIT=") {
		t.Fatal(base, env)
	}
	env = CoreEnvironment([]string{"GOMEMLIMIT=123MiB", "GOGC=90"}, ForMemory(1024*MiB))
	if len(env) != 2 || !slices.Contains(env, "GOMEMLIMIT=123MiB") || !slices.Contains(env, "GOGC=90") {
		t.Fatal(env)
	}
}
