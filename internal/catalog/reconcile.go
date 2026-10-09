// Package catalog keeps subscription selections stable across unambiguous
// provider edits. Adapted from SB Gateway 1.6.21 subscription identity rules.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Node contains only client-side connection data. Credentials must be stored
// separately from the public catalog when this package is wired to OpenWrt.
type Node struct {
	ID                string            `json:"id"`
	SelectionIdentity string            `json:"selection_identity"`
	ProviderID        string            `json:"provider_id"`
	Label             string            `json:"label"`
	Country           string            `json:"country,omitempty"`
	City              string            `json:"city,omitempty"`
	Protocol          string            `json:"protocol"`
	Transport         string            `json:"transport,omitempty"`
	Server            string            `json:"server"`
	Port              int               `json:"port"`
	Settings          map[string]any    `json:"settings,omitempty"`
	Credentials       map[string]string `json:"-"`
}

type Delta struct {
	Added           []string
	RuntimeChanged  []string
	MetadataChanged []string
	Removed         []string
}

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// Reconcile applies exact, unique endpoint and unique logical matching within
// one subscription. No fuzzy location matching is performed.
func Reconcile(providerID string, previous, incoming []Node) ([]Node, Delta, error) {
	if providerID == "" {
		return nil, Delta{}, errors.New("provider ID is required")
	}
	if len(incoming) == 0 {
		return nil, Delta{}, errors.New("empty subscription cannot replace a working catalog")
	}
	for _, node := range append(append([]Node(nil), previous...), incoming...) {
		if node.ProviderID != providerID || node.Protocol == "" || node.Server == "" || node.Port < 1 || node.Port > 65535 {
			return nil, Delta{}, errors.New("invalid or foreign subscription node")
		}
	}
	oldBase, newBase := countKeys(previous, stableIdentity), countKeys(incoming, stableIdentity)
	exactKey := func(node Node, counts map[string]int) string {
		base := stableIdentity(node)
		if counts[base] > 1 {
			return base + ":" + digest(node.Credentials)
		}
		return base
	}
	oldExactKey := func(node Node) string { return exactKey(node, oldBase) }
	newExactKey := func(node Node) string { return exactKey(node, newBase) }
	oldStable, newStable := countKeys(previous, oldExactKey), countKeys(incoming, newExactKey)
	oldEndpoint, newEndpoint := countKeys(previous, endpointIdentity), countKeys(incoming, endpointIdentity)
	oldLogical, newLogical := countKeys(previous, logicalIdentity), countKeys(incoming, logicalIdentity)
	oldExactIndex := indexKeys(previous, oldExactKey)
	oldEndpointIndex := indexKeys(previous, endpointIdentity)
	oldLogicalIndex := indexKeys(previous, logicalIdentity)
	usedOld := make([]bool, len(previous))
	usedIDs := make(map[string]bool, len(incoming))
	result := make([]Node, 0, len(incoming))
	delta := Delta{}
	for _, candidate := range incoming {
		node := candidate
		node.Settings = cloneSettings(candidate.Settings)
		node.Credentials = cloneCredentials(candidate.Credentials)
		oldIndex := -1
		stable := newExactKey(node)
		if oldStable[stable] == 1 && newStable[stable] == 1 {
			oldIndex = findIndexed(previous, usedOld, stable, oldExactIndex, node)
		}
		if oldIndex < 0 {
			endpoint := endpointIdentity(node)
			if oldEndpoint[endpoint] == 1 && newEndpoint[endpoint] == 1 {
				oldIndex = findIndexed(previous, usedOld, endpoint, oldEndpointIndex, node)
			}
		}
		if oldIndex < 0 {
			logical := logicalIdentity(node)
			if oldLogical[logical] == 1 && newLogical[logical] == 1 {
				oldIndex = findIndexed(previous, usedOld, logical, oldLogicalIndex, node)
			}
		}
		if oldIndex >= 0 && previous[oldIndex].ID != "" && !usedIDs[previous[oldIndex].ID] {
			old := previous[oldIndex]
			usedOld[oldIndex] = true
			node.ID = old.ID
			node.SelectionIdentity = old.SelectionIdentity
			if node.SelectionIdentity == "" {
				node.SelectionIdentity = oldExactKey(old)
			}
			if runtimeIdentity(old) != runtimeIdentity(node) {
				delta.RuntimeChanged = append(delta.RuntimeChanged, node.ID)
			} else if old.Label != node.Label || old.Country != node.Country || old.City != node.City {
				delta.MetadataChanged = append(delta.MetadataChanged, node.ID)
			}
		} else {
			node.ID = newNodeID(node, newBase[stableIdentity(node)] > 1)
			node.SelectionIdentity = digest([]string{providerID, stable, strings.ToUpper(node.Country)})[:24]
			base := node.ID
			for suffix := 2; usedIDs[node.ID]; suffix++ {
				node.ID = fmt.Sprintf("%s-%d", base, suffix)
			}
			delta.Added = append(delta.Added, node.ID)
		}
		usedIDs[node.ID] = true
		result = append(result, node)
	}
	for index, old := range previous {
		if !usedOld[index] {
			delta.Removed = append(delta.Removed, old.ID)
		}
	}
	return result, delta, nil
}

// Index identities once: recomputing JSON and SHA-256 for every candidate
// made a stable subscription refresh quadratic in catalog size. Ambiguous
// identities remain unusable even if one duplicate was already consumed.
func indexKeys(nodes []Node, identity func(Node) string) map[string]int {
	indexes := make(map[string]int, len(nodes))
	for i, node := range nodes {
		key := identity(node)
		if _, exists := indexes[key]; exists {
			indexes[key] = -1
		} else {
			indexes[key] = i
		}
	}
	return indexes
}

func findIndexed(previous []Node, used []bool, key string, indexes map[string]int, current Node) int {
	i, exists := indexes[key]
	if !exists || i < 0 || used[i] || knownCountryChanged(previous[i], current) {
		return -1
	}
	return i
}

func knownCountryChanged(old, current Node) bool {
	return old.Country != "" && current.Country != "" && !strings.EqualFold(old.Country, current.Country)
}

func countKeys(nodes []Node, identity func(Node) string) map[string]int {
	counts := make(map[string]int, len(nodes))
	for _, node := range nodes {
		counts[identity(node)]++
	}
	return counts
}

func stableIdentity(node Node) string {
	return digest(struct {
		Label, Protocol, Transport, Server string
		Port                               int
		Settings                           map[string]any
	}{node.Label, node.Protocol, node.Transport, node.Server, node.Port, node.Settings})[:24]
}

func endpointIdentity(node Node) string {
	return digest(struct {
		Protocol, Transport, Server string
		Port                        int
		Settings                    map[string]any
	}{node.Protocol, node.Transport, node.Server, node.Port, node.Settings})
}

func logicalIdentity(node Node) string {
	return digest([]string{node.Label, node.Protocol, node.Transport})
}

func runtimeIdentity(node Node) string {
	return digest(struct {
		Protocol, Transport, Server string
		Port                        int
		Settings                    map[string]any
		Credentials                 map[string]string
	}{node.Protocol, node.Transport, node.Server, node.Port, node.Settings, node.Credentials})
}

func newNodeID(node Node, duplicate bool) string {
	slug := strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(node.Label), "-"), "-")
	if len(slug) > 48 {
		slug = strings.TrimRight(slug[:48], "-")
	}
	if slug == "" {
		slug = "node"
	}
	identity := node.ProviderID + ":" + stableIdentity(node) + ":" + strings.ToUpper(node.Country)
	if duplicate {
		identity += digest(node.Credentials)[:12]
	}
	return slug + "-" + digest(identity)[:10]
}

func digest(value any) string {
	body, _ := json.Marshal(value) // All input types in this package are JSON-compatible.
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func cloneSettings(settings map[string]any) map[string]any {
	if settings == nil {
		return nil
	}
	body, _ := json.Marshal(settings)
	var clone map[string]any
	_ = json.Unmarshal(body, &clone)
	return clone
}

func cloneCredentials(credentials map[string]string) map[string]string {
	if credentials == nil {
		return nil
	}
	clone := make(map[string]string, len(credentials))
	for key, value := range credentials {
		clone[key] = value
	}
	return clone
}

// SortedIDs is a stable display helper; catalog order itself follows the feed.
func SortedIDs(nodes []Node) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	sort.Strings(ids)
	return ids
}
