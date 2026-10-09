package agentport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"example.org/resilient-proxy/internal/catalog"
	"example.org/resilient-proxy/internal/selection"
	"example.org/resilient-proxy/internal/settings"
	"example.org/resilient-proxy/internal/xray"
)

// WritePool publishes the full new client inventory to the unchanged SB
// Gateway health controller. Only selected/probed handlers are materialized
// in Xray. The pool contains credentials and MUST live on tmpfs with 0600.
var poolPublicationMu sync.Mutex

// PreparedPool is owned by one publication. Catalog rendering does not hold
// the publication or configuration locks; only the final atomic commit does.
type PreparedPool struct{ pool healthPool }

func WritePool(path string, nodes []catalog.Node, policies []settings.Policy) error {
	if path == "" {
		return errors.New("health pool path is required")
	}
	prepared, err := PreparePool(nodes, policies)
	if err != nil {
		return err
	}
	return prepared.Publish(path, policies)
}

// Publish overlays the latest health contract. A monitoring edit completed
// while catalog rendering was in flight must survive its later publication.
func (prepared *PreparedPool) Publish(path string, policies []settings.Policy) error {
	if prepared == nil || path == "" {
		return errors.New("prepared health pool and path are required")
	}
	poolPublicationMu.Lock()
	defer poolPublicationMu.Unlock()
	if len(prepared.pool.HealthPolicies) != len(policies) {
		return errors.New("prepared policy set changed")
	}
	for _, policy := range policies {
		tag := "resproxy-" + policy.ID
		contract, ok := prepared.pool.HealthPolicies[tag]
		if !ok || contract.Mode != policy.Mode {
			return errors.New("prepared policy definition changed")
		}
		contract.Policy = policy.Health
		prepared.pool.HealthPolicies[tag] = contract
	}
	return writeJSONAtomic(path, prepared.pool)
}

func PreparePool(nodes []catalog.Node, policies []settings.Policy) (*PreparedPool, error) {
	probeBudget := 10
	if len(policies) > 0 && policies[0].Health.ProbeBatchSize > 0 {
		probeBudget = policies[0].Health.ProbeBatchSize
	}
	pool := healthPool{
		ProbeBudget: min(probeBudget, 64), ProbeLanes: min(probeBudget, 64),
		Version: 4, Policies: make(map[string][]string),
		HealthPolicies:   make(map[string]healthPolicyContract),
		PolicyPrefixes:   make(map[string]string),
		BaseOutboundTags: []string{"block", "direct-wan"},
		Outbounds:        make(map[string]json.RawMessage, len(nodes)),
		DialTargets:      make(map[string]healthDialTarget),
	}
	fingerprints := make(map[string]string, len(nodes))
	for _, node := range nodes {
		if node.ID == "" {
			return nil, errors.New("health pool node has no ID")
		}
		body, err := xray.RenderOutbound(node)
		if err != nil {
			return nil, fmt.Errorf("render node %q: %w", node.ID, err)
		}
		pool.Outbounds[node.ID] = body
		digest := sha256.Sum256(body)
		fingerprints[node.ID] = hex.EncodeToString(digest[:])
		if strings.EqualFold(node.Protocol, "vless") && net.ParseIP(node.Server) != nil &&
			!strings.EqualFold(node.Transport, "kcp") && !strings.EqualFold(node.Transport, "quic") {
			pool.DialTargets[node.ID] = healthDialTarget{Address: node.Server, Port: node.Port}
		}
	}
	for _, configured := range policies {
		tag := "resproxy-" + configured.ID
		candidates, groups, err := selection.Candidates(nodes, configured.SelectionOrder, configured.NodeFilter)
		if err != nil {
			return nil, fmt.Errorf("policy %q selectors: %w", configured.ID, err)
		}
		contractGroups := make([]healthGroup, 0, len(configured.SelectionOrder))
		for index, selector := range configured.SelectionOrder {
			members := make([]string, 0)
			for _, candidate := range candidates {
				if groups[candidate] == index {
					members = append(members, candidate)
				}
			}
			if len(members) != 0 {
				contractGroups = append(contractGroups, healthGroup{Selector: selector, Members: members})
			}
		}
		contractNodes := make(map[string]healthNode)
		for _, node := range nodes {
			contractNodes[node.ID] = healthNode{
				SubscriptionID: node.ProviderID, Label: node.Label, Country: node.Country,
				Protocol: node.Protocol, Transport: node.Transport,
				Fingerprint: fingerprints[node.ID],
			}
		}
		policy := configured.Health
		pool.Policies[tag] = candidates
		pool.HealthPolicies[tag] = healthPolicyContract{
			Policy: policy, Mode: configured.Mode, Groups: contractGroups,
			Candidates: candidates, Nodes: contractNodes,
		}
		pool.PolicyPrefixes[tag] = "resproxy-" + configured.ID + "-"
	}
	return &PreparedPool{pool: pool}, nil
}

// RunHealth is the full source scheduler, including 3/2/15 fast lanes,
// parallel emergency probes, best/priority scoring and quality history.
func RunHealth(ctx context.Context, opts Options) error {
	if opts.HealthInterval <= 0 {
		opts.HealthInterval = 60 * time.Second
	}
	return runHealth(ctx, opts)
}
