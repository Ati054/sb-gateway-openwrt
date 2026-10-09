package daemon

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"example.org/resilient-proxy/internal/catalog"
	"example.org/resilient-proxy/internal/settings"
)

type SubscriptionUpdate struct {
	State     string    `json:"state"`
	Attempt   string    `json:"attempt,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	Nodes     int       `json:"nodes"`
	Added     int       `json:"added"`
	Removed   int       `json:"removed"`
	Error     string    `json:"error,omitempty"`
}

type subscriptionUpdates struct {
	path   string
	values map[string]SubscriptionUpdate
}

// One compact record per configured source, in tmpfs; no event history or flash writes.
func newSubscriptionUpdates(paths Paths, sources []settings.Subscription) *subscriptionUpdates {
	previous := readSubscriptionUpdates(paths)
	u := &subscriptionUpdates{path: filepath.Join(paths.RunRoot, "subscription-status.json"), values: map[string]SubscriptionUpdate{}}
	for _, source := range sources {
		value := previous[source.ID]
		if value.State == "loading" {
			value.State, value.Error = "error", "interrupted"
		}
		u.values[source.ID] = value
	}
	u.save()
	return u
}

func readSubscriptionUpdates(paths Paths) map[string]SubscriptionUpdate {
	values := map[string]SubscriptionUpdate{}
	if info, err := os.Stat(filepath.Join(paths.RunRoot, "subscription-status.json")); err == nil && info.Size() <= 16384 {
		if body, err := os.ReadFile(filepath.Join(paths.RunRoot, "subscription-status.json")); err == nil {
			_ = json.Unmarshal(body, &values)
		}
	}
	return values
}

// Counts always follow the published catalog, including after a reboot.
func SubscriptionUpdates(paths Paths, nodes []catalog.Node) map[string]SubscriptionUpdate {
	values := readSubscriptionUpdates(paths)
	counts := map[string]int{}
	for _, node := range nodes {
		counts[node.ProviderID]++
	}
	for id, value := range values {
		value.Nodes = counts[id]
		values[id] = value
	}
	for id, count := range counts {
		value := values[id]
		value.Nodes = count
		values[id] = value
	}
	return values
}

func sourceNodeIDs(id string, nodes []catalog.Node) map[string]bool {
	result := map[string]bool{}
	for _, node := range nodes {
		if node.ProviderID == id {
			result[node.ID] = true
		}
	}
	return result
}

func (u *subscriptionUpdates) begin(id string, nodes []catalog.Node) string {
	value := u.values[id]
	value.State, value.Error, value.Attempt = "loading", "", strconv.FormatInt(time.Now().UnixNano(), 10)
	value.Nodes = len(sourceNodeIDs(id, nodes))
	u.values[id] = value
	u.save()
	return value.Attempt
}

func (u *subscriptionUpdates) finish(id, attempt string, before, after []catalog.Node, err error) {
	value := u.values[id]
	value.Attempt = attempt
	current, previous := sourceNodeIDs(id, after), sourceNodeIDs(id, before)
	value.Nodes = len(current)
	if err != nil {
		value.State, value.Error = "error", "failed"
		switch {
		case strings.Contains(err.Error(), "reachable node"):
			value.Error = "unreachable"
		case strings.Contains(err.Error(), "HTTPS request"):
			value.Error = "network"
		case strings.Contains(err.Error(), "HTTP 200"):
			value.Error = "server"
		}
	} else {
		value.State, value.Error, value.UpdatedAt = "success", "", time.Now().UTC()
		value.Added, value.Removed = 0, 0
		for node := range current {
			if !previous[node] {
				value.Added++
			}
		}
		for node := range previous {
			if !current[node] {
				value.Removed++
			}
		}
	}
	u.values[id] = value
	u.save()
}

func (u *subscriptionUpdates) save() {
	body, err := json.Marshal(u.values)
	if err == nil {
		err = os.WriteFile(u.path+".tmp", body, 0600)
	}
	if err == nil {
		err = os.Rename(u.path+".tmp", u.path)
	}
	if err != nil {
		log.Printf("resproxy: subscription status unavailable: %v", err)
	}
}
