package selection

import (
	"net/url"
	"sort"
	"strings"

	"example.org/resilient-proxy/internal/catalog"
)

type Location struct {
	Value   string `json:"value"`
	Label   string `json:"label"`
	Count   int    `json:"count"`
	Country string `json:"country"`
	City    string `json:"city,omitempty"`
}

type Choice struct {
	ID           string `json:"id"`
	Value        string `json:"value"`
	Label        string `json:"label"`
	Country      string `json:"country"`
	City         string `json:"city"`
	Subscription string `json:"subscription"`
	Protocol     string `json:"protocol"`
}

type Inventory struct {
	Regions   map[string]map[string]bool `json:"regions"`
	Locations []Location                 `json:"locations"`
	Nodes     []Choice                   `json:"nodes"`
}

// Public picker data contains no endpoints, transport settings or credentials.
// Nodes retain their order in the current combined subscription catalog.
func PublicInventory(nodes []catalog.Node) Inventory {
	result := Inventory{Regions: regions, Locations: []Location{}, Nodes: []Choice{}}
	byValue := map[string]*Location{}
	for _, node := range nodes {
		country := strings.ToUpper(node.Country)
		if node.SelectionIdentity != "" {
			result.Nodes = append(result.Nodes, Choice{ID: node.ID, Value: "location:" + node.SelectionIdentity, Label: node.Label, Country: country, City: node.City, Subscription: node.ProviderID, Protocol: node.Protocol})
		}
		if country == "" {
			continue
		}
		add := func(value, label, city string) {
			if entry := byValue[value]; entry != nil {
				entry.Count++
			} else {
				byValue[value] = &Location{Value: value, Label: label, Country: country, City: city, Count: 1}
			}
		}
		add("country:"+country, country, "")
		if node.City != "" {
			add("city:"+country+":"+url.PathEscape(node.City), country+" / "+strings.TrimPrefix(node.City, country+" "), node.City)
		}
	}
	for _, item := range byValue {
		result.Locations = append(result.Locations, *item)
	}
	sort.Slice(result.Locations, func(i, j int) bool { return result.Locations[i].Value < result.Locations[j].Value })
	return result
}
