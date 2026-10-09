package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"sync"
	"time"
)

// Serializes routing publications while allowing selector updates during compilation.
var routingMu sync.Mutex

func balancerTags(config BaseConfig) []string {
	tags := make([]string, 0, len(config.Policies)+config.ProbeLaneCount()+1)
	for _, policy := range config.Policies {
		tags = append(tags, "resproxy-"+policy.ID)
	}
	tags = append(tags, HealthProbeTags(config)...)
	tags = append(tags, RefreshProbeTag)
	sort.Strings(tags)
	return tags
}

// routingDocument makes the current selected handler the *base* selector of
// each replacement balancer. The bundled core carries the latest override at
// publication. This base selector also provides a fail-closed fallback.
func routingDocument(config BaseConfig, selected map[string]string) ([]byte, error) {
	body, err := RenderBase(config)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	routing := document["routing"].(map[string]any)
	for _, value := range routing["balancers"].([]any) {
		balancer := value.(map[string]any)
		tag := balancer["tag"].(string)
		if selected[tag] != "" {
			balancer["selector"] = []string{selected[tag]}
		}
	}
	return json.Marshal(document)
}

// PublishRouting replaces route rules in the running core, preserving every
// policy and probe lane. The caller persists the new ruleset only after this
// returns successfully. On failure, the previous routing is reapplied.
func (m *Manager) PublishRouting(ctx context.Context, previous, next BaseConfig) error {
	routingMu.Lock()
	defer routingMu.Unlock()
	if m.Binary == "" || m.Server == "" || m.Run == nil {
		return errors.New("incomplete Xray routing configuration")
	}
	if err := previous.Validate(); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if reflect.DeepEqual(previous, next) {
		return nil // Do not rebuild an unchanged routing table.
	}
	previousTags, nextTags := balancerTags(previous), balancerTags(next)
	if len(previousTags) != len(nextTags) {
		return errors.New("hot routing change cannot add or remove route lists")
	}
	for index := range previousTags {
		if previousTags[index] != nextTags[index] {
			return errors.New("hot routing change cannot rename route lists")
		}
	}
	// Snapshot and initialize overrides in one short critical section. The core
	// preserves changes made after this snapshot, including during rollback.
	selected, err := m.routingSelections(ctx, nextTags)
	if err != nil {
		return err
	}
	oldBody, err := routingDocument(previous, selected)
	if err != nil {
		return err
	}
	newBody, err := routingDocument(next, selected)
	if err != nil {
		return err
	}
	if err := m.applyRouting(ctx, newBody, selected, RoutingTimeout(next)); err == nil {
		return nil
	} else {
		// A cancelled update must still restore the prior routing. Keep the
		// routing publication lock until a bounded rollback is confirmed.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 330*time.Second)
		defer cancel()
		if rollbackErr := m.applyRouting(rollbackCtx, oldBody, selected, RoutingTimeout(previous)); rollbackErr != nil {
			return fmt.Errorf("Xray routing state uncertain: update failed: %v; rollback failed: %w", err, rollbackErr)
		}
		return fmt.Errorf("Xray routing update rolled back: %w", err)
	}
}

func (m *Manager) applyRouting(ctx context.Context, body []byte, selected map[string]string, budget time.Duration) error {
	file, err := os.CreateTemp(m.TempDir, "resproxyd-routing-*.json")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// adrules has its own three-second RPC deadline unless --timeout is set.
	// A selected country can exceed that on a 256 MiB router even when the
	// routing document is valid. Keep the outer process budget slightly longer
	// than the RPC deadline so failures are returned by Xray, not a killed CLI.
	rpcSeconds := 25
	if budget > 30*time.Second {
		rpcSeconds = 300
	}
	callCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if _, err := m.Run(callCtx, m.Binary, "api", "adrules", fmt.Sprintf("--timeout=%d", rpcSeconds), "--server="+m.Server, path); err != nil {
		if callCtx.Err() != nil {
			return fmt.Errorf("Xray routing API exceeded %s: %w", budget, callCtx.Err())
		}
		return fmt.Errorf("Xray rejected replacement routing rules: %w", err)
	}
	// Do not reapply the pre-compilation snapshot: an emergency switch may
	// have selected a newer handler while adrules was compiling.
	MutationMu.Lock()
	defer MutationMu.Unlock()
	for _, tag := range balancerTagsFromSelected(selected) {
		actual, err := m.current(ctx, tag)
		if err != nil || actual == "" {
			return fmt.Errorf("selector %q was not confirmed after routing update", tag)
		}
	}

	return nil
}

func balancerTagsFromSelected(selected map[string]string) []string {
	tags := make([]string, 0, len(selected))
	for tag := range selected {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func (m *Manager) routingSelections(ctx context.Context, tags []string) (map[string]string, error) {
	MutationMu.Lock()
	defer MutationMu.Unlock()
	selected := make(map[string]string, len(tags))
	for _, tag := range tags {
		current, err := m.current(ctx, tag)
		if err != nil {
			return nil, fmt.Errorf("read selector %q before routing update: %w", tag, err)
		}
		if current == "" {
			current = "block"
			if err := m.override(ctx, tag, current); err != nil {
				return nil, err
			}
		}
		selected[tag] = current
	}
	return selected, nil
}
