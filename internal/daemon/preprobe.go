package daemon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/selection"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
	"github.com/Ati054/sb-gateway-openwrt/internal/subscription"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

const refreshProbeBatch = 20

// chooseAfterProbe keeps the current handler when its wire definition is
// unchanged. A changed/new endpoint is first tried through a dedicated HTTP
// inbound and balancer, so a failed candidate never replaces live traffic.
func chooseAfterProbe(ctx context.Context, next, previous []catalog.Node, config settings.Config, paths Paths, manager *xray.Manager, cursor *int) (string, error) {
	if manager == nil || cursor == nil {
		return "", errors.New("isolated refresh probe is unavailable")
	}
	candidates, err := defaultCandidates(next, config)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		// Import must work before a route list selects its first location.
		// Publish the catalog/pool without changing the live selector; health
		// retains fail-closed behavior until a configured candidate is proven.
		return "", nil
	}
	selector := "resproxy-" + config.Base.DefaultPolicy
	currentTag, err := manager.Current(ctx, selector)
	if err != nil {
		return "", errors.New("live selector state cannot be verified")
	}
	oldByID := make(map[string]catalog.Node, len(previous))
	active := activeNode(filepath.Join(paths.RunRoot, "health-state", "selector-health.json"), selector)
	for _, node := range previous {
		oldByID[node.ID] = node
		definition, err := xray.RenderOutbound(node)
		if err == nil && xray.DynamicTag(selector, node.ID, definition) == currentTag {
			active = node.ID
		}
	}
	nextByID := make(map[string]catalog.Node, len(next))
	for _, node := range next {
		nextByID[node.ID] = node
	}
	if node, ok := nextByID[active]; ok && containsID(candidates, active) {
		oldDefinition, oldErr := xray.RenderOutbound(oldByID[active])
		newDefinition, newErr := xray.RenderOutbound(node)
		if oldErr == nil && newErr == nil && bytes.Equal(oldDefinition, newDefinition) &&
			xray.DynamicTag(selector, active, newDefinition) == currentTag {
			*cursor = 0
			return active, nil
		}
	}
	start := *cursor % len(candidates)
	ordered := append(append([]string(nil), candidates[start:]...), candidates[:start]...)
	if active != "" && containsID(ordered, active) {
		ordered = append([]string{active}, withoutID(ordered, active)...)
	}
	limit := min(refreshProbeBatch, len(ordered))
	for _, id := range ordered[:limit] {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if probeCandidate(ctx, manager, config.Base.ProbeBasePort+config.Base.ProbeLaneCount(), nextByID[id]) {
			*cursor = 0
			return id, nil
		}
	}
	*cursor = (start + limit) % len(candidates)
	return "", errors.New("no reachable node in the current refresh probe batch; existing route retained")
}

func containsID(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func withoutID(values []string, excluded string) []string {
	result := make([]string, 0, len(values)-1)
	for _, value := range values {
		if value != excluded {
			result = append(result, value)
		}
	}
	return result
}

func probeCandidate(ctx context.Context, manager *xray.Manager, port int, node catalog.Node) bool {
	definition, err := xray.RenderOutbound(node)
	if err != nil {
		return false
	}
	result, err := manager.Switch(ctx, xray.RefreshProbeTag, node.ID, definition)
	if err != nil {
		return false
	}
	if result.Changed && strings.HasPrefix(result.PreviousTag, xray.RefreshProbeTag+"-") {
		_ = manager.Retire(ctx, xray.RefreshProbeTag, result.PreviousTag)
	}
	for _, target := range []struct {
		URL    string
		Status int
	}{
		{"https://www.gstatic.com/generate_204", http.StatusNoContent},
		{"https://www.cloudflare.com/cdn-cgi/trace", http.StatusOK},
	} {
		attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
		request, err := http.NewRequestWithContext(attempt, http.MethodGet, target.URL, nil)
		if err == nil {
			client := subscription.NewHTTPClient(port)
			response, requestErr := client.Do(request)
			if requestErr == nil {
				response.Body.Close()
				if response.StatusCode == target.Status {
					cancel()
					return true
				}
			}
			client.CloseIdleConnections()
		}
		cancel()
	}
	return false
}

func defaultCandidates(nodes []catalog.Node, config settings.Config) ([]string, error) {
	var configured *settings.Policy
	for i := range config.Policies {
		if config.Policies[i].ID == config.Base.DefaultPolicy {
			configured = &config.Policies[i]
			break
		}
	}
	if configured == nil {
		return nil, errors.New("default route list is not defined")
	}
	candidates, _, err := selection.Candidates(nodes, configured.SelectionOrder, configured.NodeFilter)
	return candidates, err
}
