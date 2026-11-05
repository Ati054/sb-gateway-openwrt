package agentport

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Supply producer timestamps and overlapping two-lane waves to the real
// controller. The model makes no claims about physical router throughput.
type cadenceQualityRuntime struct {
	*fakeSelectorRuntime
	at   time.Time
	wave uint64
}

func (*cadenceQualityRuntime) qualityProbeParallelism() int { return 2 }

func (r *cadenceQualityRuntime) ProbeQualityParallel(nodes []string) map[string]probeEvidence {
	r.wave++
	result := make(map[string]probeEvidence, len(nodes))
	for _, node := range nodes {
		e := r.fakeSelectorRuntime.Probe(node)
		e.ObservedAt, e.QualityWave = r.at, r.wave
		e.QualityStartedAt = r.at.Add(-time.Second)
		result[node] = e
	}
	return result
}

func cadenceController(t *testing.T, count int) (*healthController, *cadenceQualityRuntime, *policyHealthState) {
	return cadenceControllerWithDelays(t, count, 600, 200)
}

func cadenceControllerWithDelays(t *testing.T, count, activeMS, reserveMS int) (*healthController, *cadenceQualityRuntime, *policyHealthState) {
	t.Helper()
	c, item, base := stagedOptimizationController(t)
	clearOptimizationCandidate(item)
	contract := base.pool.HealthPolicies["europe"]
	contract.Policy.SwitchImprovementMS = 200
	for index := 2; index < count; index++ {
		node := fmt.Sprintf("other-%02d", index)
		contract.Candidates = append(contract.Candidates, node)
		contract.Nodes[node] = healthNode{Label: node}
		item.AvailabilityOK[node], item.QualityOK[node], item.Recoveries[node] = true, true, 3
		item.LastProbeAt[node] = 943 // Due in the wave after the first proof.
		base.probes[node] = successfulEvidence(900)
	}
	base.pool.ProbeBudget = 2
	base.pool.HealthPolicies["europe"] = contract
	item.Mode, item.CandidateSignature, item.CandidateNodes = "best", strings.Join(contract.Candidates, "\n"), contract.Nodes
	for node, delay := range map[string]int{"active": activeMS, "reserve": reserveMS} {
		value := delay
		item.LastProbeAt[node] = 940
		item.Samples[node] = []healthSample{{At: 940, OK: true, DelayMS: &value}}
		base.probes[node] = successfulEvidence(delay)
	}
	r := &cadenceQualityRuntime{fakeSelectorRuntime: base, at: time.Unix(1000, 0)}
	c.runtime = r
	if err := c.Tick(r.at); err != nil {
		t.Fatal(err)
	}
	if item.Selected != "active" || item.OptimizationChecks != 1 || item.OptimizationCandidate != "reserve" {
		t.Fatalf("first full pair was not staged: selected=%s checks=%d candidate=%s calls=%v", item.Selected, item.OptimizationChecks, item.OptimizationCandidate, base.probeCalls)
	}
	return c, r, item
}

func TestURLTestInterleavedInventoryCannotAccelerateConfirmation(t *testing.T) {
	for _, count := range []int{3, 7, 10, 24} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c, r, item := cadenceController(t, count)
			for second := int64(1003); second < 1060; second += 3 {
				r.at, r.probeCalls = time.Unix(second, 0), nil
				if err := c.Tick(r.at); err != nil {
					t.Fatal(err)
				}
				if contains(r.probeCalls, "reserve") || len(r.probeCalls) > 2 || item.Selected != "active" || item.OptimizationChecks != 1 {
					t.Fatalf("t=%d accelerated a complete proof: calls=%v selected=%s checks=%d pair=%+v", second, r.probeCalls, item.Selected, item.OptimizationChecks, item.qualityPair)
				}
			}
			// A previously admitted peer can finish first; it must not defer
			// this candidate for another quality interval.
			for second := int64(1060); second <= 1063 && item.Selected == "active"; second += 3 {
				r.at, r.probeCalls = time.Unix(second, 0), nil
				if err := c.Tick(r.at); err != nil {
					t.Fatal(err)
				}
			}
			if item.Selected != "reserve" || len(r.probeCalls) != 2 {
				t.Fatalf("ordinary second confirmation was delayed: selected=%s calls=%v", item.Selected, r.probeCalls)
			}
		})
	}
}

func TestURLTestRestartCannotAccelerateConfirmation(t *testing.T) {
	c, r, _ := cadenceController(t, 3)
	r.at, r.probeCalls = time.Unix(1007, 0), nil
	// A new xraySelectorRuntime reports a generation reset on its first
	// successful Reload, even when it reconnects to an unchanged core.
	r.reset = true
	restarted := &healthController{opts: c.opts, runtime: r}
	if err := restarted.Tick(r.at); err != nil {
		t.Fatal(err)
	}
	item := restarted.state["europe"]
	if item.Selected != "active" {
		t.Fatalf("restart counted a second proof after seven seconds: selected=%s calls=%v", item.Selected, r.probeCalls)
	}
}

func TestURLTestOtherFasterReservesDoNotReplacePendingConfirmation(t *testing.T) {
	for _, count := range []int{3, 7, 10, 24} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c, r, item := cadenceController(t, count)
			for node := range r.probes {
				if node != "active" && node != "reserve" {
					r.probes[node] = successfulEvidence(100)
				}
			}
			for second := int64(1003); second <= 1063 && item.Selected == "active"; second += 3 {
				r.at, r.probeCalls = time.Unix(second, 0), nil
				if err := c.Tick(r.at); err != nil {
					t.Fatal(err)
				}
				if item.Selected == "active" && (item.OptimizationCandidate != "reserve" || item.OptimizationChecks != 1) {
					t.Fatalf("t=%d another wave replaced a qualifying pending reserve: candidate=%s checks=%d", second, item.OptimizationCandidate, item.OptimizationChecks)
				}
			}
			if item.Selected != "reserve" {
				t.Fatalf("other qualifying reserves starved confirmation: selected=%s", item.Selected)
			}
		})
	}
}

func TestInitialRuntimeReloadInvalidatesRestoredConfirmations(t *testing.T) {
	path := t.TempDir() + "/pool.json"
	if err := writeJSONAtomic(path, healthFixture(false)); err != nil {
		t.Fatal(err)
	}
	r := newXraySelectorRuntime(Options{HealthPoolFile: path})
	for index := 0; index < 2; index++ {
		_, reset, err := r.Reload()
		if err != nil || reset != (index == 0) {
			t.Fatalf("reload %d: reset=%v error=%v", index, reset, err)
		}
	}
}

func TestURLTestUnrelatedWaveCannotDisqualifyPendingReserve(t *testing.T) {
	for _, count := range []int{3, 7, 10, 24} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c, r, item := cadenceControllerWithDelays(t, count, 900, 500)
			// A common 400ms delay clears for active/B. Pending C still has
			// its old measurement, not a comparable loss against the new A.
			for node := range r.probes {
				delay := 100
				if node == "active" {
					delay = 500
				}
				r.probes[node] = successfulEvidence(delay)
			}
			for second := int64(1003); second < 1060; second += 3 {
				r.at = time.Unix(second, 0)
				if err := c.Tick(r.at); err != nil {
					t.Fatal(err)
				}
				if item.Selected != "active" || item.OptimizationCandidate != "reserve" || item.OptimizationChecks != 1 {
					t.Fatalf("t=%d unrelated wave disqualified a pending reserve using stale timing: selected=%s candidate=%s checks=%d", second, item.Selected, item.OptimizationCandidate, item.OptimizationChecks)
				}
			}
			for second := int64(1060); second <= 1063 && item.OptimizationChecks > 0; second += 3 {
				r.at = time.Unix(second, 0)
				if err := c.Tick(r.at); err != nil {
					t.Fatal(err)
				}
			}
			// The fresh pair no longer passes the existing median criterion.
			// Unlike an unrelated wave, this real loss must revoke the proof.
			if item.OptimizationChecks != 0 || item.OptimizationCandidate != "" {
				t.Fatal("a comparable loss could not release the pending confirmation")
			}
		})
	}
}
