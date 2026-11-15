package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestDNSFirewallMatchesLiveNFTablesFixtures(t *testing.T) {
	body, err := os.ReadFile("testdata/nft-dns-live.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string          `json:"name"`
		Snapshot json.RawMessage `json:"snapshot"`
		Sets     []*nftPortSet   `json:"sets"`
		Want     bool            `json:"want"`
	}
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			reads := 0
			got, err := dnsFirewallConflictJSON(tc.Snapshot, func(id nftSetIdentity) (*nftPortSet, error) {
				reads++
				for _, set := range tc.Sets {
					if set.nftSetIdentity == id {
						return set, nil
					}
				}
				return nil, errors.New("unexpected set query")
			})
			if err != nil || got != tc.Want || reads != len(tc.Sets) {
				t.Fatalf("got=%v err=%v set reads=%d; want=%v sets=%d", got, err, reads, tc.Want, len(tc.Sets))
			}
		})
	}
}

func TestDNSFirewallSetFailureIsNotEvidenceOfNoConflict(t *testing.T) {
	body := []byte(`{"nftables":[{"set":{"family":"inet","table":"other","name":"ports","type":"inet_service"}},{"rule":{"family":"inet","table":"other","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":"@ports"}},{"redirect":{"port":5454}}]}}]}`)
	failure := errors.New("set disappeared")
	if conflict, err := dnsFirewallConflictJSON(body, func(nftSetIdentity) (*nftPortSet, error) { return nil, failure }); conflict || !errors.Is(err, failure) {
		t.Fatalf("set lookup error treated as no conflict: %v, %v", conflict, err)
	}
}

func TestDNSFirewallIgnoresOwnTablesAndUnrelatedLargeIPSet(t *testing.T) {
	body := []byte(`{"nftables":[{"set":{"family":"inet","table":"fw4","name":"large_routes","type":"ipv4_addr"}},{"rule":{"family":"ip","table":"resproxy","expr":[{"redirect":{"port":1053}}]}},{"rule":{"family":"ip6","table":"resproxy6","expr":[{"tproxy":{"port":1053}}]}}]}`)
	conflict, err := dnsFirewallConflictJSON(body, func(nftSetIdentity) (*nftPortSet, error) {
		t.Fatal("unrelated IP set elements were requested")
		return nil, nil
	})
	if err != nil || conflict {
		t.Fatalf("own tables treated as foreign: %v, %v", conflict, err)
	}
}

func TestDNSFirewallRejectsIncompleteJSON(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"nftables":null}`, `{"nftables":[`} {
		if _, err := dnsFirewallConflictJSON([]byte(body), nil); err == nil {
			t.Errorf("incomplete snapshot accepted: %s", body)
		}
	}
}

func TestDNSFirewallOwnTableIdentityIncludesFamily(t *testing.T) {
	for _, tc := range []struct {
		family, table string
		want          bool
	}{{"ip", "resproxy", false}, {"ip6", "resproxy6", false}, {"inet", "resproxy", true}, {"ip6", "resproxy", true}, {"ip", "resproxy6", true}} {
		body, err := json.Marshal(map[string]any{"nftables": []any{map[string]any{"rule": map[string]any{"family": tc.family, "table": tc.table, "expr": []any{map[string]any{"redirect": map[string]any{"port": 5454}}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := dnsFirewallConflictJSON(body, nil); err != nil || got != tc.want {
			t.Errorf("%s/%s: got=%v error=%v want=%v", tc.family, tc.table, got, err, tc.want)
		}
	}
}

func TestDNSFirewallDoesNotTreatBitmaskOperatorAsEquality(t *testing.T) {
	body := []byte(`{"nftables":[{"rule":{"family":"inet","table":"other","expr":[{"match":{"op":"in","left":{"payload":{"protocol":"udp","field":"dport"}},"right":1}},{"redirect":{"port":5454}}]}}]}`)
	if _, err := dnsFirewallConflictJSON(body, nil); err == nil {
		t.Fatal("unsupported bitmask comparison was mistaken for equality")
	}
}

func TestDNSFirewallCompatibilityTargetsAreNotSilentlyIgnored(t *testing.T) {
	for _, tc := range []struct {
		name string
		port int
		want bool
		fail bool
	}{{"REDIRECT", 53, true, false}, {"DNAT", 53, true, false}, {"TPROXY", 443, false, false}, {"UNKNOWN", 53, false, true}, {"UNKNOWN", 443, false, false}} {
		body, err := json.Marshal(map[string]any{"nftables": []any{map[string]any{"rule": map[string]any{"family": "inet", "table": "other", "expr": []any{
			map[string]any{"match": map[string]any{"op": "==", "left": map[string]any{"payload": map[string]any{"protocol": "udp", "field": "dport"}}, "right": tc.port}},
			map[string]any{"xt": map[string]any{"type": "target", "name": tc.name}},
		}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := dnsFirewallConflictJSON(body, nil); got != tc.want || (err != nil) != tc.fail {
			t.Errorf("%s/%d: got=%v error=%v", tc.name, tc.port, got, err)
		}
	}
}
