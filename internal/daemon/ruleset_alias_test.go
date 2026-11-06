package daemon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
	"github.com/Ati054/sb-gateway-openwrt/internal/rulesets"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

type aliasRulesetRuntime struct{ published chan xray.BaseConfig }

func (r *aliasRulesetRuntime) PublishRouting(_ context.Context, _, next xray.BaseConfig) error {
	r.published <- next
	return nil
}

func TestRulesetReplacementThroughOpenWrtRuntimeAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix runtime directory alias")
	}
	paths, config, initial := progressFixture(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tmp", "run", "resproxy"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tmp", filepath.Join(root, "var")); err != nil {
		t.Fatal(err)
	}
	paths.RunRoot = filepath.Join(root, "var", "run", "resproxy")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	routing := &aliasRulesetRuntime{published: make(chan xray.BaseConfig, 2)}
	go func() {
		defer close(done)
		calls := 0
		runRulesetUpdatesUsing(ctx, config, paths, routing, initial, config.Base, nil,
			func(context.Context, []string, map[string]rulesets.CountrySelection, rulesets.Snapshot, int) (rulesets.Snapshot, []rulesets.PackStatus, error) {
				calls++
				domains := []string{"old.example.test"}
				if calls > 1 {
					domains = []string{"new.example.test", "extra.example.test"}
				}
				return rulesets.Snapshot{Version: rulesets.SnapshotVersion, UpdatedAt: time.Now(), Packs: map[string]rulesets.Rules{"youtube": {Suffix: domains}}}, []rulesets.PackStatus{{ID: "youtube", State: "updated", Entries: len(domains)}}, nil
			})
	}()
	defer func() { cancel(); <-done }()
	first := awaitProgress(t, paths, "ready")
	firstBase := <-routing.published
	if err := os.WriteFile(filepath.Join(paths.RunRoot, "rulesets-refresh.request"), []byte("refresh"), 0600); err != nil {
		t.Fatal(err)
	}
	// This asserts eventual publication, not disk latency. Allow slow fsync on
	// virtualized storage while retaining a bounded failure for a stuck worker.
	deadline := time.Now().Add(30 * time.Second)
	for {
		status := readProgress(t, paths)
		if status.Phase == "ready" && status.CheckedAt.After(first.CheckedAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("changed ruleset was not published through the alias path")
		}
		time.Sleep(10 * time.Millisecond)
	}
	secondBase := <-routing.published
	firstRef := firstBase.Policies[0].Rules[0].ServiceDomains[0]
	secondRef := secondBase.Policies[0].Rules[0].ServiceDomains[0]
	if firstRef == secondRef {
		t.Fatal("runtime retained the old rules instead of the downloaded replacement")
	}
	stored, err := rulesets.Load(rulesetSnapshotPath(paths))
	if err != nil || stored.Packs["youtube"].Count() != 2 {
		t.Fatal("replacement snapshot did not persist")
	}
	name, ok := geoipasset.DomainReferenceName(stored.Packs["youtube"].Reference)
	if !ok {
		t.Fatal("replacement asset reference is invalid")
	}
	body, err := os.ReadFile(filepath.Join(paths.DataRoot, "geoip-assets", name))
	if err != nil || !bytes.Contains(body, []byte("new.example.test")) || bytes.Contains(body, []byte("old.example.test")) {
		t.Fatal("immutable database did not contain the new domains")
	}
}
