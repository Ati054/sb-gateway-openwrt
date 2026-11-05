package rulesets

import "testing"

func TestPendingSelectedExpandsOnlyDownloadableSources(t *testing.T) {
	previous := Snapshot{Packs: map[string]Rules{"youtube": {Reference: "stored-reference", Entries: 7}}, Countries: map[string]CountryRules{"ru": {GeoIP: []string{"203.0.113.0/24"}}}}
	status, err := PendingSelected([]string{"youtube", "youtube"}, map[string]CountrySelection{"ru": {GeoIP: true}}, previous)
	if err != nil || len(status) != 2 {
		t.Fatalf("pending sources: %+v %v", status, err)
	}
	for _, item := range status {
		if item.State != "cached" || item.Entries == 0 || item.ID == "geosite:ru" {
			t.Fatalf("unrequested or cached source: %+v", item)
		}
	}
	status, err = PendingSelected([]string{"ru-telecom"}, nil, Snapshot{})
	if err != nil || len(status) == 0 {
		t.Fatalf("aggregate sources: %+v %v", status, err)
	}
	for _, item := range status {
		if item.ID == "ru-telecom" || item.State != "pending" {
			t.Fatalf("aggregate needs no own download: %+v", item)
		}
	}
}
