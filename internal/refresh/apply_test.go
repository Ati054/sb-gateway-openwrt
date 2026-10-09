package refresh

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"example.org/resilient-proxy/internal/catalog"
	"example.org/resilient-proxy/internal/generation"
	"example.org/resilient-proxy/internal/xray"
)

const linkCA = "vless://2f1c08fc-3f43-4e95-8f57-f8bded750a1a@ca.example.test:443?security=tls#CA%20Toronto"
const linkUS = "vless://96f81313-e2c0-4a16-bbb6-9e54d393761b@us.example.test:443?security=tls#US%20Chicago"

type fakeRuntime struct {
	calls        int
	restored     string
	fail         bool
	afterSwitch  func()
	previousTag  string
	lastSelected string
}

func (r *fakeRuntime) Switch(_ context.Context, _, nodeID string, outbound json.RawMessage) (xray.SwitchResult, error) {
	r.calls++
	r.lastSelected = nodeID
	if !strings.Contains(string(outbound), `"protocol":"vless"`) {
		return xray.SwitchResult{}, errors.New("bad outbound")
	}
	if r.fail {
		return xray.SwitchResult{}, errors.New("simulated API failure")
	}
	if r.afterSwitch != nil {
		r.afterSwitch()
	}
	return xray.SwitchResult{Changed: true, SelectedTag: "new-tag", PreviousTag: r.previousTag}, nil
}

func (r *fakeRuntime) Restore(_ context.Context, _, tag string) error {
	r.restored = tag
	return nil
}

func firstNode(nodes []catalog.Node) (string, error) { return nodes[0].ID, nil }

func TestApplyPublishesAfterSwitchAndPreservesOtherProvider(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	runtime := &fakeRuntime{previousTag: "block"}
	service := Service{Store: store, Runtime: runtime}
	first, err := service.Apply(context.Background(), "provider-ca", []byte(linkCA), 10, "main", firstNode)
	if err != nil || first.OldTag != "block" {
		t.Fatalf("first refresh: %+v %v", first, err)
	}
	second, err := service.Apply(context.Background(), "provider-us", []byte(linkUS), 10, "main", func(nodes []catalog.Node) (string, error) {
		if len(nodes) != 2 {
			t.Fatalf("other provider disappeared: %+v", nodes)
		}
		return nodes[1].ID, nil
	})
	if err != nil || second.GenerationID == first.GenerationID {
		t.Fatalf("second refresh: %+v %v", second, err)
	}
	_, nodes, err := store.Current()
	if err != nil || len(nodes) != 2 || runtime.calls != 2 {
		t.Fatalf("incomplete multi-provider publication: %+v %v, calls=%d", nodes, err, runtime.calls)
	}
}

func TestApplyFailureDoesNotReplaceCurrent(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	runtime := &fakeRuntime{previousTag: "block"}
	service := Service{Store: store, Runtime: runtime}
	first, err := service.Apply(context.Background(), "provider", []byte(linkCA), 10, "main", firstNode)
	if err != nil {
		t.Fatal(err)
	}
	runtime.fail = true
	if _, err := service.Apply(context.Background(), "provider", []byte(linkUS), 10, "main", firstNode); err == nil {
		t.Fatal("Xray failure was ignored")
	}
	current, _, err := store.Current()
	if err != nil || current != first.GenerationID {
		t.Fatalf("failed switch displaced working catalog: %s %v", current, err)
	}
	if _, err := service.Apply(context.Background(), "provider", []byte("trojan://invalid"), 10, "main", firstNode); err == nil || runtime.calls != 2 {
		t.Fatalf("bad subscription reached runtime: %v, calls=%d", err, runtime.calls)
	}
}

func TestApplyRestoresSelectorIfPublishFails(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	runtime := &fakeRuntime{previousTag: "old-live-tag"}
	runtime.afterSwitch = func() {
		if err := os.Mkdir(filepath.Join(store.Root, "CURRENT"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	service := Service{Store: store, Runtime: runtime}
	if _, err := service.Apply(context.Background(), "provider", []byte(linkCA), 10, "main", firstNode); err == nil {
		t.Fatal("pointer publish failure was ignored")
	}
	if runtime.restored != "old-live-tag" {
		t.Fatalf("old selector not restored: %q", runtime.restored)
	}
}

func TestApplyRestoresSelectorIfPoolUpdateFails(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	runtime := &fakeRuntime{previousTag: "old-live-tag"}
	service := Service{Store: store, Runtime: runtime,
		AfterSwitch: func(_, _ []catalog.Node) (func() error, error) {
			return nil, errors.New("simulated tmpfs pool failure")
		},
	}
	if _, err := service.Apply(context.Background(), "provider", []byte(linkCA), 10, "main", firstNode); err == nil {
		t.Fatal("pool failure was ignored")
	}
	if runtime.restored != "old-live-tag" {
		t.Fatalf("live selector was not restored: %q", runtime.restored)
	}
	if _, _, err := store.Current(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed pool update published a generation: %v", err)
	}
}

func TestApplyRollsBackPoolIfPublishFails(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	runtime := &fakeRuntime{previousTag: "old-live-tag"}
	runtime.afterSwitch = func() {
		if err := os.Mkdir(filepath.Join(store.Root, "CURRENT"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	updated, rolledBack := false, false
	service := Service{Store: store, Runtime: runtime,
		AfterSwitch: func(_, _ []catalog.Node) (func() error, error) {
			updated = true
			return func() error { rolledBack = true; return nil }, nil
		},
	}
	if _, err := service.Apply(context.Background(), "provider", []byte(linkCA), 10, "main", firstNode); err == nil {
		t.Fatal("publish failure was ignored")
	}
	if !updated || !rolledBack || runtime.restored != "old-live-tag" {
		t.Fatal("pool and live selector were not both rolled back")
	}
}

func TestValidContractionPublishesWithoutApproval(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	runtime := &fakeRuntime{previousTag: "block"}
	service := Service{Store: store, Runtime: runtime}
	var feed strings.Builder
	for index := 0; index < 12; index++ {
		feed.WriteString(strings.ReplaceAll(linkCA, "ca.example.test", "node"+string(rune('a'+index))+".example.test"))
		feed.WriteByte('\n')
	}
	first, err := service.Apply(context.Background(), "provider", []byte(feed.String()), 30, "main", firstNode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), "provider", []byte("not a subscription"), 30, "main", firstNode); err == nil {
		t.Fatal("invalid response replaced the working catalog")
	}
	current, _, err := store.Current()
	if err != nil || current != first.GenerationID || runtime.calls != 1 {
		t.Fatalf("invalid response damaged current generation: %s %v, calls=%d", current, err, runtime.calls)
	}
	if _, err := service.Apply(context.Background(), "provider", []byte(linkCA), 30, "main", firstNode); err != nil {
		t.Fatalf("valid contraction failed: %v", err)
	}
	_, nodes, err := store.Current()
	if err != nil || len(nodes) != 1 || runtime.calls != 2 {
		t.Fatalf("valid contraction not published: %d nodes, %d calls, %v", len(nodes), runtime.calls, err)
	}
}

func TestConcurrentProvidersDoNotOverwriteEachOther(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	service := &Service{Store: store, Runtime: &fakeRuntime{previousTag: "block"}}
	var group sync.WaitGroup
	errors := make(chan error, 2)
	for _, item := range []struct{ provider, feed string }{{"provider-ca", linkCA}, {"provider-us", linkUS}} {
		group.Add(1)
		go func(provider, feed string) {
			defer group.Done()
			_, err := service.Apply(context.Background(), provider, []byte(feed), 10, "main", firstNode)
			errors <- err
		}(item.provider, item.feed)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	_, nodes, err := store.Current()
	if err != nil || len(nodes) != 2 {
		t.Fatalf("concurrent providers lost a generation: %+v %v", nodes, err)
	}
}

func TestImportBeforeLocationSelectionDoesNotSwitchTraffic(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	runtime := &fakeRuntime{fail: true}
	pools := 0
	service := Service{Store: store, Runtime: runtime, AfterSwitch: func(_, next []catalog.Node) (func() error, error) {
		pools++
		if len(next) != pools {
			t.Fatalf("unexpected imported inventory: %d", len(next))
		}
		return func() error { return nil }, nil
	}}
	noSelection := func([]catalog.Node) (string, error) { return "", nil }
	for _, source := range []struct{ id, feed string }{{"ca", linkCA}, {"us", linkUS}} {
		result, err := service.Apply(context.Background(), source.id, []byte(source.feed), 10, "main", noSelection)
		if err != nil || result.SelectedID != "" || result.OldTag != "" {
			t.Fatalf("import without route: %+v %v", result, err)
		}
	}
	_, nodes, err := store.Current()
	if err != nil || len(nodes) != 2 || runtime.calls != 0 || pools != 2 {
		t.Fatalf("unselected import: nodes=%d calls=%d pools=%d err=%v", len(nodes), runtime.calls, pools, err)
	}
	if _, err = service.Apply(context.Background(), "ca", []byte("invalid"), 10, "main", noSelection); err == nil || pools != 2 {
		t.Fatal("invalid feed changed catalog/pool")
	}
}

func TestUnselectedImportRollsBackPoolOnPublicationFailure(t *testing.T) {
	store := generation.Store{Root: t.TempDir()}
	runtime := &fakeRuntime{fail: true}
	rolledBack := false
	service := Service{Store: store, Runtime: runtime, AfterSwitch: func(_, _ []catalog.Node) (func() error, error) {
		if err := os.Mkdir(filepath.Join(store.Root, "CURRENT"), 0700); err != nil {
			t.Fatal(err)
		}
		return func() error { rolledBack = true; return nil }, nil
	}}
	_, err := service.Apply(context.Background(), "ca", []byte(linkCA), 10, "main", func([]catalog.Node) (string, error) { return "", nil })
	if err == nil || !rolledBack || runtime.calls != 0 {
		t.Fatalf("unselected publication rollback: %v rollback=%v calls=%d", err, rolledBack, runtime.calls)
	}
}
