package xray

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

type fakeXray struct {
	selected   string
	added      []string
	removed    []string
	operations []string
	ignoreNext bool
	failBO     bool
}

func (fake *fakeXray) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) < 2 || args[0] != "api" {
		return nil, errors.New("unexpected command")
	}
	fake.operations = append(fake.operations, args[1])
	switch args[1] {
	case "bi":
		return []byte("Selecting Override:\n  - " + fake.selected + "\n- Selects:\n"), nil
	case "ado":
		body, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		var config struct {
			Outbounds []struct {
				Tag string `json:"tag"`
			} `json:"outbounds"`
		}
		if err := json.Unmarshal(body, &config); err != nil || len(config.Outbounds) != 1 {
			return nil, errors.New("invalid add payload")
		}
		fake.added = append(fake.added, config.Outbounds[0].Tag)
		return nil, nil
	case "bo":
		if fake.failBO {
			return nil, errors.New("temporary API error")
		}
		if fake.ignoreNext {
			fake.ignoreNext = false
			return nil, nil
		}
		fake.selected = args[len(args)-1]
		return nil, nil
	case "rmo":
		fake.removed = append(fake.removed, args[len(args)-1])
		return nil, nil
	case "lso":
		available := map[string]bool{"old-tag": true}
		for _, tag := range fake.added {
			available[tag] = true
		}
		for _, tag := range fake.removed {
			delete(available, tag)
		}
		outbounds := make([]map[string]string, 0, len(available))
		for tag := range available {
			outbounds = append(outbounds, map[string]string{"tag": tag})
		}
		return json.Marshal(map[string]any{"outbounds": outbounds})
	}
	return nil, errors.New("unexpected action")
}

func TestSwitchAddsBeforeOverrideAndConfirmsBeforeRetirement(t *testing.T) {
	fake := &fakeXray{selected: "old-tag"}
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	manager.Run = fake.run
	definition := json.RawMessage(`{"protocol":"freedom","sendThrough":"127.0.0.2"}`)
	result, err := manager.Switch(context.Background(), "group-a", "node-a", definition)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.PreviousTag != "old-tag" || result.SelectedTag != fake.selected {
		t.Fatalf("wrong result: %+v", result)
	}
	if !slices.Equal(fake.operations, []string{"bi", "lso", "ado", "lso", "bo", "bi"}) || len(fake.removed) != 0 {
		t.Fatalf("unsafe sequence: %v removed=%v", fake.operations, fake.removed)
	}
	if err := manager.Retire(context.Background(), "group-a", result.SelectedTag); err == nil {
		t.Fatal("selected handler was retired")
	}
	if err := manager.Retire(context.Background(), "group-a", result.PreviousTag); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.removed, []string{"old-tag"}) {
		t.Fatalf("retired handlers: %v", fake.removed)
	}
	if err := manager.Retire(context.Background(), "group-a", result.PreviousTag); err != nil || len(fake.removed) != 1 {
		t.Fatalf("repeated retirement was not idempotent: %v removed=%v", err, fake.removed)
	}
	fake.operations = nil
	second, err := manager.Switch(context.Background(), "group-a", "node-a", definition)
	if err != nil || second.Changed || !slices.Equal(fake.operations, []string{"bi", "lso"}) {
		t.Fatalf("unchanged handler was recreated: %+v %v %v", second, err, fake.operations)
	}
}

func TestSwitchRestoresSelectedHandlerMissingFromXray(t *testing.T) {
	fake := &fakeXray{selected: "old-tag"}
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	manager.Run = fake.run
	definition := json.RawMessage(`{"protocol":"freedom"}`)
	first, err := manager.Switch(context.Background(), "group-a", "node-a", definition)
	if err != nil {
		t.Fatal(err)
	}
	fake.added = nil // Simulate Xray losing the selected handler.
	fake.operations = nil
	second, err := manager.Switch(context.Background(), "group-a", "node-a", definition)
	if err != nil || second.Changed || second.SelectedTag != first.SelectedTag {
		t.Fatalf("missing selected handler was not restored: %+v %v", second, err)
	}
	if !slices.Equal(fake.operations, []string{"bi", "lso", "ado", "lso"}) {
		t.Fatalf("unexpected restore sequence: %v", fake.operations)
	}
}

func TestSwitchRestoresPreviousAfterSilentOverrideFailure(t *testing.T) {
	fake := &fakeXray{selected: "old-tag", ignoreNext: true}
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	manager.Run = fake.run
	_, err := manager.Switch(context.Background(), "group-a", "node-a", json.RawMessage(`{"protocol":"freedom"}`))
	if err == nil || fake.selected != "old-tag" || len(fake.removed) != 1 {
		t.Fatalf("failed override was not rolled back: selected=%q removed=%v err=%v", fake.selected, fake.removed, err)
	}
	if !slices.Equal(fake.operations, []string{"bi", "lso", "ado", "lso", "bo", "bi", "bo", "bi", "rmo"}) {
		t.Fatalf("wrong rollback sequence: %v", fake.operations)
	}
}

func TestSwitchReportsUncertainStateWhenRollbackCannotBeConfirmed(t *testing.T) {
	fake := &fakeXray{selected: "old-tag", failBO: true}
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	manager.Run = fake.run
	_, err := manager.Switch(context.Background(), "group-a", "node-a", json.RawMessage(`{"protocol":"freedom"}`))
	if err == nil || !strings.Contains(err.Error(), "uncertain") || len(fake.removed) != 0 {
		t.Fatalf("uncertain state was hidden: err=%v removed=%v", err, fake.removed)
	}
}

func TestDynamicTagTracksOutboundDefinition(t *testing.T) {
	a := DynamicTag("group-a", "node-a", json.RawMessage(`{"protocol":"freedom","sendThrough":"127.0.0.2"}`))
	b := DynamicTag("group-a", "node-a", json.RawMessage(`{"sendThrough":"127.0.0.2","protocol":"freedom"}`))
	c := DynamicTag("group-a", "node-a", json.RawMessage(`{"protocol":"freedom","sendThrough":"127.0.0.3"}`))
	if a != b || a == c {
		t.Fatalf("version tag is unstable: %q %q %q", a, b, c)
	}
	if !strings.HasPrefix(a, "group-a-") {
		t.Fatalf("tag does not match health-controller namespace: %q", a)
	}
}

func TestSwitchStopsWhenXrayInventoryCannotDrain(t *testing.T) {
	fake := &fakeXray{selected: "old-tag"}
	for index := 0; index < maxDynamicOutboundsPerSelector; index++ {
		fake.added = append(fake.added, "group-a-"+strings.Repeat("a", 11)+string(rune('0'+index)))
	}
	manager := NewManager("xray", "127.0.0.1:10085", t.TempDir())
	manager.Run = fake.run
	_, err := manager.Switch(context.Background(), "group-a", "new-node", json.RawMessage(`{"protocol":"freedom"}`))
	if err == nil || !strings.Contains(err.Error(), "inventory limit") || fake.selected != "old-tag" ||
		slices.Contains(fake.operations, "ado") {
		t.Fatalf("unbounded handler add or route change: err=%v selected=%s operations=%v", err, fake.selected, fake.operations)
	}
}
