package rulesets

import "sort"

// PendingSelected describes only downloadable components referenced by routes.
// Aggregate cards and built-in IP seeds do not need their own downloaded copy.
func PendingSelected(selected []string, countries map[string]CountrySelection, snapshot Snapshot) ([]PackStatus, error) {
	known, err := index()
	if err != nil {
		return nil, err
	}
	ids, err := selectedPacks(selected, known)
	if err != nil {
		return nil, err
	}
	result := make([]PackStatus, 0, len(ids)+2*len(countries))
	add := func(id string, entries int) {
		state := "pending"
		if entries > 0 {
			state = "cached"
		}
		result = append(result, PackStatus{ID: id, State: state, Entries: entries})
	}
	for _, id := range ids {
		if known[id].UpstreamName != "" {
			add(id, snapshot.Packs[id].Count())
		}
	}
	for country, choice := range countries {
		if choice.Geosite {
			add("geosite:"+country, snapshot.Countries[country].Geosite.Count())
		}
		if choice.GeoIP {
			add("geoip:"+country, len(snapshot.Countries[country].GeoIP))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
