package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/resources"
	"github.com/Ati054/sb-gateway-openwrt/internal/rulesets"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

func progressFixture(t *testing.T) (Paths, settings.Config, rulesets.Snapshot) {
	t.Helper()
	paths := Paths{RunRoot: t.TempDir(), DataRoot: t.TempDir()}
	base := xray.DefaultBaseConfig()
	base.Policies[0].Rules = []xray.RouteRule{{ID: "video", Action: "direct", ServicePacks: []string{"youtube"}}}
	config := settings.Config{Base: base, Policies: []settings.Policy{{ID: base.Policies[0].ID}}, RulesetUpdateMode: "automatic", RulesetUpdateSeconds: 86400}
	return paths, config, rulesets.Snapshot{Version: rulesets.SnapshotVersion, Packs: map[string]rulesets.Rules{}}
}

func readProgress(t *testing.T, paths Paths) rulesetStatus {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(paths.RunRoot, "rulesets-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value rulesetStatus
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func awaitProgress(t *testing.T, paths Paths, phase string) rulesetStatus {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(filepath.Join(paths.RunRoot, "rulesets-status.json"))
		var status rulesetStatus
		if err == nil && json.Unmarshal(body, &status) == nil && status.Phase == phase {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("rule database phase did not reach %q", phase)
	return rulesetStatus{}
}

type progressRouting struct {
	t     *testing.T
	paths Paths
	calls int
}

func (r *progressRouting) PublishRouting(_ context.Context, _, next xray.BaseConfig) error {
	if readProgress(r.t, r.paths).Phase != "applying" || len(next.Policies[0].Rules[0].ServiceDomains) == 0 {
		r.t.Error("new rules were not prepared before runtime publication")
	}
	r.calls++
	return nil
}

func TestRulesetProgressLateAttachmentDownloadsAutomatically(t *testing.T) {
	paths, config, initial := progressFixture(t)
	// The first Apply had an unreferenced rule group and no downloadable bases.
	before := config
	before.RuleGroups = config.Base.Policies[0].Rules
	before.Base = xray.DefaultBaseConfig()
	if monitoringOnly(before, config) || captureOnly(before, config) {
		t.Fatal("attaching a rule did not require reloading the route configuration")
	}
	runRulesetUpdates(context.Background(), before, paths, nil, initial, before.Base, nil)
	if readProgress(t, paths).Phase != "unused" {
		t.Fatal("unreferenced rule was treated as a pending download")
	}
	if initialRulesetDelay(initialRulesetProgress(config, initial), nil) != 0 {
		t.Fatal("late attachment still waits for the initial update interval")
	}
	// The next Apply binds the rule. The real worker must download, compile,
	// publish and persist it, without a refresh request or manual button.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runtime := &progressRouting{t: t, paths: paths}
	go func() {
		defer close(done)
		runRulesetUpdatesUsing(ctx, config, paths, runtime, initial, config.Base, nil,
			func(context.Context, []string, map[string]rulesets.CountrySelection, rulesets.Snapshot, int) (rulesets.Snapshot, []rulesets.PackStatus, error) {
				if readProgress(t, paths).Phase != "downloading" {
					t.Error("download was not announced")
				}
				return rulesets.Snapshot{Version: rulesets.SnapshotVersion, UpdatedAt: time.Now(), Packs: map[string]rulesets.Rules{"youtube": {Suffix: []string{"example.test"}}}}, []rulesets.PackStatus{{ID: "youtube", State: "updated", Entries: 1}}, nil
			})
	}()
	defer func() { cancel(); <-done }()
	awaitProgress(t, paths, "ready")
	cancel()
	<-done
	if runtime.calls != 1 {
		t.Fatalf("runtime publications=%d", runtime.calls)
	}
	stored, err := rulesets.Load(rulesetSnapshotPath(paths))
	if err != nil || stored.Packs["youtube"].Count() != 1 {
		t.Fatalf("automatic download was not persisted: %v", err)
	}
}

func TestRulesetProgressQueueCancellationPreservesSnapshot(t *testing.T) {
	paths, config, initial := progressFixture(t)
	release, err := resources.BeginUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRulesetUpdatesUsing(ctx, config, paths, nil, initial, config.Base, nil,
			func(context.Context, []string, map[string]rulesets.CountrySelection, rulesets.Snapshot, int) (rulesets.Snapshot, []rulesets.PackStatus, error) {
				t.Error("queued download ran despite cancellation")
				return initial, nil, errors.New("unexpected fetch")
			})
	}()
	defer func() { cancel(); release(); <-done }()
	awaitProgress(t, paths, "queued")
	cancel()
	<-done
	if _, err := os.Stat(rulesetSnapshotPath(paths)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled queue replaced the snapshot")
	}
}

func TestRulesetProgressCachedAndNewCountryKinds(t *testing.T) {
	_, config, snapshot := progressFixture(t)
	snapshot.Packs["youtube"] = rulesets.Rules{Entries: 4, Reference: "stored-reference"}
	if delay := initialRulesetDelay(initialRulesetProgress(config, snapshot), nil); delay != 120*time.Second {
		t.Fatalf("cached periodic check delay=%s", delay)
	}
	config.Base.Policies[0].Rules[0].Country = "ru"
	config.Base.Policies[0].Rules[0].UseGeoIP = true
	_, countries := selectedRulesetSources(config)
	if initialRulesetDelay(initialRulesetProgress(config, snapshot), countries) != 0 {
		t.Fatal("a newly added country kind was not fetched immediately")
	}
	snapshot.Countries = map[string]rulesets.CountryRules{"ru": {GeoIP: []string{"203.0.113.0/24"}}}
	if initialRulesetDelay(initialRulesetProgress(config, snapshot), countries) != 5*time.Second {
		t.Fatal("unrequested GeoSite was considered missing")
	}
}

func TestRulesetProgressFailedDownloadKeepsLastGoodRules(t *testing.T) {
	paths, config, initial := progressFixture(t)
	initial.Packs["youtube"] = rulesets.Rules{Suffix: []string{"old.example"}}
	if err := rulesets.Save(rulesetSnapshotPath(paths), initial); err != nil {
		t.Fatal(err)
	}
	config.Base.Policies[0].Rules[0].Country = "ru"
	config.Base.Policies[0].Rules[0].UseGeoIP = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRulesetUpdatesUsing(ctx, config, paths, nil, initial, config.Base, nil,
			func(context.Context, []string, map[string]rulesets.CountrySelection, rulesets.Snapshot, int) (rulesets.Snapshot, []rulesets.PackStatus, error) {
				return rulesets.Snapshot{}, nil, errors.New("upstream unavailable")
			})
	}()
	defer func() { cancel(); <-done }()
	status := awaitProgress(t, paths, "error")
	if status.Error != "upstream unavailable" {
		t.Fatalf("error was hidden: %+v", status)
	}
	stored, err := rulesets.Load(rulesetSnapshotPath(paths))
	if err != nil || len(stored.Packs["youtube"].Suffix) != 1 || stored.Packs["youtube"].Suffix[0] != "old.example" {
		t.Fatalf("failed download replaced existing rules: %+v %v", stored, err)
	}
}
