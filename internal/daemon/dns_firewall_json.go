package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type nftSetIdentity struct {
	Family string `json:"family"`
	Table  string `json:"table"`
	Name   string `json:"name"`
}

type nftPortSet struct {
	nftSetIdentity
	Type any   `json:"type"`
	Elem []any `json:"elem"`
}

type nftDNSRule struct {
	Family string           `json:"family"`
	Table  string           `json:"table"`
	Expr   []map[string]any `json:"expr"`
}

type nftDNSObject struct {
	Set  *nftPortSet `json:"set"`
	Rule *nftDNSRule `json:"rule"`
}

type nftDNSDocument struct {
	Objects []nftDNSObject `json:"nftables"`
}

type nftDNSInspection struct {
	sets  map[nftSetIdentity]*nftPortSet
	cache map[nftSetIdentity][]any
	load  func(nftSetIdentity) (*nftPortSet, error)
}

func currentDNSFirewallConflict(ctx context.Context) (bool, error) {
	return inspectDNSFirewall(ctx, "nft")
}

func inspectDNSFirewall(ctx context.Context, binary string) (bool, error) {
	// One budget covers the structural snapshot and all referenced port sets.
	// Unrelated IP/domain sets are never enumerated.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body, err := readNFTInspection(ctx, binary, nil, "-n", "-s", "-t", "-j", "list", "ruleset")
	if err != nil {
		return false, err
	}
	return dnsFirewallConflictJSON([]byte(body), func(id nftSetIdentity) (*nftPortSet, error) {
		request, err := json.Marshal(map[string]any{"nftables": []any{map[string]any{"list": map[string]any{"set": id}}}})
		if err != nil {
			return nil, err
		}
		// JSON input keeps quoted kernel identifiers out of nft command text.
		raw, err := readNFTInspection(ctx, binary, strings.NewReader(string(request)), "-n", "-s", "-j", "-f", "-")
		if err != nil {
			return nil, err
		}
		var doc nftDNSDocument
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return nil, fmt.Errorf("decode DNS port set: %w", err)
		}
		var found *nftPortSet
		for _, object := range doc.Objects {
			if object.Set != nil {
				if found != nil || object.Set.nftSetIdentity != id {
					return nil, errors.New("DNS port set identity changed during inspection")
				}
				found = object.Set
			}
		}
		if found == nil {
			return nil, errors.New("DNS port set disappeared during inspection")
		}
		return found, nil
	})
}

func dnsFirewallConflictJSON(body []byte, load func(nftSetIdentity) (*nftPortSet, error)) (bool, error) {
	var doc nftDNSDocument
	if err := json.Unmarshal(body, &doc); err != nil || doc.Objects == nil {
		return false, fmt.Errorf("incomplete nftables structural snapshot: %v", err)
	}
	inspection := nftDNSInspection{sets: map[nftSetIdentity]*nftPortSet{}, cache: map[nftSetIdentity][]any{}, load: load}
	for _, object := range doc.Objects {
		if object.Set != nil {
			inspection.sets[object.Set.nftSetIdentity] = object.Set
		}
	}
	for _, object := range doc.Objects {
		rule := object.Rule
		if rule == nil || rule.Family == "ip" && rule.Table == "resproxy" || rule.Family == "ip6" && rule.Table == "resproxy6" {
			continue
		}
		intercepts := false
		uncertainAction := false
		for _, expression := range rule.Expr {
			for _, action := range []string{"redirect", "dnat", "tproxy"} {
				if _, ok := expression[action]; ok {
					intercepts = true
				}
			}
			if value, exists := expression["xt"]; exists {
				compat, ok := value.(map[string]any)
				if ok && compat["type"] == "match" {
					continue
				}
				name, _ := compat["name"].(string)
				switch strings.ToUpper(name) {
				case "REDIRECT", "DNAT", "TPROXY":
					intercepts = true
				default:
					uncertainAction = true
				}
			}
		}
		if !intercepts && !uncertainAction {
			continue
		}
		possible, err := inspection.ruleMayMatchDNS(*rule)
		if err != nil {
			return false, err
		}
		if possible {
			if uncertainAction && !intercepts {
				return false, errors.New("unresolved compatibility target in DNS path")
			}
			return true, nil
		}
	}
	return false, nil
}

func (inspection *nftDNSInspection) ruleMayMatchDNS(rule nftDNSRule) (bool, error) {
	var uncertain error
	for _, expression := range rule.Expr {
		match, ok := expression["match"].(map[string]any)
		if !ok || !nftContainsDPort(match["left"]) {
			continue
		}
		payload, direct := nftPayload(match["left"])
		if !direct {
			uncertain = errors.New("unsupported composite DNS destination-port expression")
			continue
		}
		if protocol, _ := payload["protocol"].(string); protocol != "tcp" && protocol != "udp" && protocol != "th" {
			return false, nil
		}
		value, err := inspection.portPredicate(rule, match)
		if err != nil {
			uncertain = err
			continue
		}
		if !value {
			return false, nil
		}
	}
	return uncertain == nil, uncertain
}

func nftPayload(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	payload, ok := object["payload"].(map[string]any)
	return payload, ok && payload["field"] == "dport"
}

func nftContainsDPort(value any) bool {
	if _, ok := nftPayload(value); ok {
		return true
	}
	switch item := value.(type) {
	case map[string]any:
		for _, child := range item {
			if nftContainsDPort(child) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if nftContainsDPort(child) {
				return true
			}
		}
	}
	return false
}

func (inspection *nftDNSInspection) portPredicate(rule nftDNSRule, match map[string]any) (bool, error) {
	right := match["right"]
	name := ""
	if text, ok := right.(string); ok && strings.HasPrefix(text, "@") {
		name = strings.TrimPrefix(text, "@")
	} else if object, ok := right.(map[string]any); ok {
		name, _ = object["set"].(string)
	}
	if name != "" {
		id := nftSetIdentity{rule.Family, rule.Table, name}
		elements, ok := inspection.cache[id]
		if !ok {
			meta := inspection.sets[id]
			if meta == nil || meta.Type != "inet_service" || inspection.load == nil {
				return false, errors.New("unresolved DNS destination-port set")
			}
			if len(inspection.cache) >= 16 {
				return false, errors.New("DNS destination-port set query limit exceeded")
			}
			full, err := inspection.load(id)
			if err != nil {
				return false, err
			}
			if full == nil || full.nftSetIdentity != id || full.Type != "inet_service" || len(full.Elem) > 65536 {
				return false, errors.New("DNS destination-port set changed or exceeds its limit")
			}
			elements = full.Elem
			inspection.cache[id] = elements
		}
		right = map[string]any{"set": elements}
	}
	op, _ := match["op"].(string)
	if op == "==" || op == "!=" {
		member, err := nftPortMember(right, 53)
		if op == "!=" {
			member = !member
		}
		return member, err
	}
	port, ok := nftPortNumber(right)
	if !ok {
		return false, errors.New("unsupported DNS destination-port comparison")
	}
	switch op {
	case "<":
		return 53 < port, nil
	case ">":
		return 53 > port, nil
	case "<=":
		return 53 <= port, nil
	case ">=":
		return 53 >= port, nil
	}
	return false, errors.New("unsupported DNS destination-port operator")
}

func nftPortNumber(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && number >= 0 && number <= 65535 && number == float64(uint16(number))
}

func nftPortMember(value any, port float64) (bool, error) {
	if number, ok := nftPortNumber(value); ok {
		return number == port, nil
	}
	if object, ok := value.(map[string]any); ok {
		if span, ok := object["range"].([]any); ok && len(span) == 2 {
			low, lok := nftPortNumber(span[0])
			high, hok := nftPortNumber(span[1])
			if lok && hok && low <= high {
				return low <= port && port <= high, nil
			}
		}
		if elements, ok := object["set"].([]any); ok {
			for _, element := range elements {
				member, err := nftPortMember(element, port)
				if err != nil || member {
					return member, err
				}
			}
			return false, nil
		}
		if element, ok := object["elem"].(map[string]any); ok {
			return nftPortMember(element["val"], port)
		}
	}
	return false, errors.New("unsupported DNS destination-port value")
}
