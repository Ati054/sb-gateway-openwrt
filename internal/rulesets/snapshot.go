package rulesets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxSnapshotSize = 32 << 20
const SnapshotVersion = 2

// Snapshot is a complete, validated candidate. The caller applies it to Xray
// before committing it to flash, so download failures never replace live rules.
type Snapshot struct {
	Version   int                     `json:"version"`
	UpdatedAt time.Time               `json:"updated_at"`
	Packs     map[string]Rules        `json:"packs"`
	Countries map[string]CountryRules `json:"countries,omitempty"`
}

type PackStatus struct {
	ID      string `json:"id"`
	State   string `json:"state"` // updated, unchanged, stale, or unavailable
	Entries int    `json:"entries"`
	Error   string `json:"error,omitempty"`
}

func Load(path string) (Snapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > maxSnapshotSize {
		return Snapshot{}, errors.New("ruleset snapshot is oversized")
	}
	var value Snapshot
	decoder := json.NewDecoder(io.LimitReader(file, maxSnapshotSize+1))
	if err := decoder.Decode(&value); err != nil {
		return Snapshot{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Snapshot{}, errors.New("ruleset snapshot has trailing data")
	}
	if err := value.Validate(); err != nil {
		return Snapshot{}, err
	}
	return value, nil
}

func (s Snapshot) Validate() error {
	if s.Version != SnapshotVersion || len(s.Countries) > len(countryGeositeRoot) {
		return errors.New("ruleset snapshot has an invalid version or pack count")
	}
	known, err := index()
	if err != nil {
		return err
	}
	if len(s.Packs) > len(known) {
		return errors.New("ruleset snapshot exceeds the catalog pack count")
	}
	for id, rules := range s.Packs {
		if known[id].ID == "" || rules.Count() == 0 || rules.Count() > maxEntries {
			return fmt.Errorf("invalid snapshot pack %q", id)
		}
		for _, domain := range append(append([]string{}, rules.Exact...), rules.Suffix...) {
			if !validDomain(domain) {
				return fmt.Errorf("invalid snapshot domain in %q", id)
			}
		}
		for _, keyword := range rules.Keyword {
			if keyword == "" || len(keyword) > 1024 {
				return fmt.Errorf("invalid snapshot keyword in %q", id)
			}
		}
		for _, expression := range rules.Regex {
			if expression == "" || len(expression) > 1024 {
				return fmt.Errorf("invalid snapshot regex in %q", id)
			}
			if _, err := regexp.Compile(expression); err != nil {
				return fmt.Errorf("invalid snapshot regex in %q: %w", id, err)
			}
		}
	}
	for country, rules := range s.Countries {
		if countryGeositeRoot[country] == "" || len(rules.GeoIP) > maxGeoIPEntries {
			return fmt.Errorf("invalid country snapshot %q", country)
		}
		for _, domain := range append(append([]string{}, rules.Geosite.Exact...), rules.Geosite.Suffix...) {
			if !validDomain(domain) {
				return fmt.Errorf("invalid GeoSite domain in %q", country)
			}
		}
		for _, keyword := range rules.Geosite.Keyword {
			if keyword == "" || len(keyword) > 1024 {
				return fmt.Errorf("invalid GeoSite keyword in %q", country)
			}
		}
		for _, expression := range rules.Geosite.Regex {
			if expression == "" || len(expression) > 1024 {
				return fmt.Errorf("invalid GeoSite regex in %q", country)
			}
			if _, err := regexp.Compile(expression); err != nil {
				return fmt.Errorf("invalid GeoSite regex in %q: %w", country, err)
			}
		}
		for _, raw := range rules.GeoIP {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil || prefix.Bits() == 0 || prefix.Masked() != prefix {
				return fmt.Errorf("invalid GeoIP CIDR in %q", country)
			}
		}
	}
	return nil
}

// Build fetches only selected cards and their dependencies. A failed pack
// retains its last good downloaded copy.
func Build(ctx context.Context, selected []string, previous Snapshot, fetch Fetch) (Snapshot, []PackStatus, error) {
	known, err := index()
	if err != nil {
		return Snapshot{}, nil, err
	}
	ids, err := selectedPacks(selected, known)
	if err != nil {
		return Snapshot{}, nil, err
	}
	result := Snapshot{Version: SnapshotVersion, UpdatedAt: time.Now().UTC(), Packs: map[string]Rules{}}
	statuses := make([]PackStatus, 0, len(ids))
	for _, id := range ids {
		pack := known[id]
		if pack.UpstreamName == "" {
			continue
		} // Aggregate cards fetch their children.
		status := PackStatus{ID: id, State: "unavailable"}
		if pack.UpstreamName != "" {
			compiled, fetchErr := Compile(ctx, pack.UpstreamName, fetch)
			if fetchErr == nil {
				result.Packs[id] = compiled
				status.State = "updated"
				if sameRules(previous.Packs[id], compiled) {
					status.State = "unchanged"
				}
				status.Entries = compiled.Count()
			} else {
				status.Error = fetchErr.Error()
				if old := previous.Packs[id]; old.Count() > 0 {
					status.State = "stale"
					result.Packs[id] = old
					status.Entries = old.Count()
				}
			}
		}
		statuses = append(statuses, status)
	}
	if err := result.Validate(); err != nil {
		return Snapshot{}, nil, err
	}
	return result, statuses, nil
}

func selectedPacks(selected []string, known map[string]pack) ([]string, error) {
	allowed := map[string]bool{}
	for _, id := range featuredIDs {
		allowed[id] = true
	}
	// Retired API-only cards remain valid in previously saved rules. Their parent
	// cards include these dependencies for all newly created rules.
	for _, id := range []string{"okx-api", "bybit-api", "gateio-api"} {
		allowed[id] = true
	}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visited[id] {
			return nil
		}
		item := known[id]
		if item.ID == "" {
			return fmt.Errorf("unknown service pack %q", id)
		}
		visited[id] = true
		for _, child := range item.IncludedPackIDs {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range selected {
		if !allowed[id] {
			return nil, fmt.Errorf("unknown selectable service pack %q", id)
		}
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(visited))
	for id := range visited {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// Expressions combines downloaded data with reviewed dependency seeds.
func Expressions(selected []string, snapshot Snapshot) ([]string, error) {
	known, err := index()
	if err != nil {
		return nil, err
	}
	ids, err := selectedPacks(selected, known)
	if err != nil {
		return nil, err
	}
	values := map[string]bool{}
	for _, id := range ids {
		rules := snapshot.Packs[id]
		for _, domain := range known[id].FallbackDomains {
			if !validDomain(domain) {
				return nil, fmt.Errorf("invalid reviewed domain in %s", id)
			}
			values["domain:"+domain] = true
		}
		for _, group := range []struct {
			prefix string
			values []string
		}{
			{"full:", rules.Exact}, {"domain:", rules.Suffix},
			{"keyword:", rules.Keyword}, {"regexp:", rules.Regex},
		} {
			for _, value := range group.values {
				values[group.prefix+value] = true
			}
		}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func Save(path string, snapshot Snapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(snapshot)
	if err != nil || len(body) > maxSnapshotSize {
		return errors.New("ruleset snapshot cannot be encoded within size limit")
	}
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, body) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".rulesets-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func sameRules(a, b Rules) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func SelectedFromPolicies(policies [][]string) []string {
	seen := map[string]bool{}
	for _, selected := range policies {
		for _, id := range selected {
			seen[strings.TrimSpace(id)] = true
		}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// ServiceCIDRs keeps service-owned destinations (e.g. Telegram) independent of
// DNS names; it never turns them into device/source restrictions.
func ServiceCIDRs(selected []string) ([]string, error) {
	known, err := index()
	if err != nil {
		return nil, err
	}
	ids, err := selectedPacks(selected, known)
	if err != nil {
		return nil, err
	}
	values := map[string]bool{}
	for _, id := range ids {
		for _, value := range known[id].IPCIDRs {
			prefix, err := netip.ParsePrefix(value)
			if err != nil || prefix.Masked().String() != value {
				return nil, fmt.Errorf("invalid reviewed service CIDR in %s", id)
			}
			values[value] = true
		}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}
