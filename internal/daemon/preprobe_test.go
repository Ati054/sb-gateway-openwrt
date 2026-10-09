package daemon

import (
	"context"
	"reflect"
	"testing"

	"example.org/resilient-proxy/internal/catalog"
	"example.org/resilient-proxy/internal/selection"
	"example.org/resilient-proxy/internal/settings"
	"example.org/resilient-proxy/internal/xray"
)

func TestRefreshProbesUseDefaultListsOwnFilterAfterReordering(t *testing.T) {
	config := settings.Config{Base: xray.BaseConfig{DefaultPolicy: "main"}, Policies: []settings.Policy{
		{ID: "office", SelectionOrder: []string{"country:US"}},
		{ID: "main", SelectionOrder: []string{"all:*"}, NodeFilter: selection.Filter{Countries: []string{"CA"}, Include: "premium"}},
	}}
	nodes := []catalog.Node{{ID: "wrong-list", Country: "US", Label: "premium"}, {ID: "wrong-filter", Country: "CA", Label: "trial"}, {ID: "selected", Country: "CA", Label: "premium"}}
	got, err := defaultCandidates(nodes, config)
	if err != nil || !reflect.DeepEqual(got, []string{"selected"}) {
		t.Fatalf("refresh probes=%v err=%v", got, err)
	}
}

func TestNoEligibleLocationImportsWithoutProbeOrSelectorChange(t *testing.T) {
	config := settings.Config{Base: xray.BaseConfig{DefaultPolicy: "main"}, Policies: []settings.Policy{{ID: "main", SelectionOrder: []string{"all:*"}, NodeFilter: selection.Filter{Include: "not selected yet"}}}}
	cursor := 7
	id, err := chooseAfterProbe(context.Background(), []catalog.Node{{ID: "node", Label: "Available location"}}, nil, config, Paths{}, &xray.Manager{}, &cursor)
	if err != nil || id != "" || cursor != 7 {
		t.Fatalf("unselected bootstrap: id=%q cursor=%d err=%v", id, cursor, err)
	}
	config.Base.DefaultPolicy = "missing"
	if _, err := chooseAfterProbe(context.Background(), nil, nil, config, Paths{}, &xray.Manager{}, &cursor); err == nil {
		t.Fatal("invalid default route silently accepted")
	}
}
