package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestSnapshotPreservesPolicyOrderAndLists(t *testing.T) {
	var packages map[string]map[string]map[string]any
	err := json.Unmarshal([]byte(`{"resproxy":{
 "cfgZ":{".type":"policy",".index":9,"id":"all","mode":"best"},
 "cfgA":{".type":"policy",".index":12,"id":"pc","mode":"priority","selection_order":["location:FR:Strasbourg","country:PL"],"source_cidrs":["192.0.2.1/32"]},
 "main":{".type":"main",".index":0,"enabled":"1"}
 },"network":{"lan":{".type":"interface",".index":0,"device":"br-lan"}}}`), &packages)
	if err != nil {
		t.Fatal(err)
	}
	get, err := snapshotGetter(packages)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadUCI(context.Background(), get)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Policies) != 2 || cfg.Policies[0].ID != "all" || cfg.Policies[1].ID != "pc" {
		t.Fatalf("policies lost or reordered: %+v", cfg.Policies)
	}
	if !reflect.DeepEqual(cfg.Policies[1].SelectionOrder, []string{"location:FR:Strasbourg", "country:PL"}) {
		t.Fatal("selection order lost")
	}
}

func TestReadUCIRejectsInterruptedPartialConfiguration(t *testing.T) {
	for _, key := range []string{"resproxy.main.enabled", "resproxy.@rule[0].id", "resproxy.@policy[1].id"} {
		t.Run(key, func(t *testing.T) {
			get := fake(map[string]string{"resproxy.@policy[0].id": "all"})
			cfg, err := ReadUCI(context.Background(), func(ctx context.Context, k string) (string, error) {
				if k == key {
					return "", context.DeadlineExceeded
				}
				return get(ctx, k)
			})
			if !errors.Is(err, context.DeadlineExceeded) || len(cfg.Policies) != 0 {
				t.Fatalf("accepted partial configuration: %v %+v", err, cfg.Policies)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadUCI(ctx, fake(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestSnapshotRejectsIncompleteOrMalformedReply(t *testing.T) {
	for _, section := range []map[string]any{
		{".type": "policy"}, {".index": float64(0)},
		{".index": float64(0), ".type": "policy", "id": []any{1}},
	} {
		if _, err := snapshotGetter(map[string]map[string]map[string]any{"resproxy": {"bad": section}}); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}
