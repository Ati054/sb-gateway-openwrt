package agentport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/dnsroute"
)

func TestDNSRoutePublicationRequiresActualProductionReadback(t *testing.T) {
	const policy = "resproxy-main"
	routes := dnsroute.New([]string{policy})
	core := dnsroute.Core{PID: 42, StartTicks: "10"}
	routes.SetCore(core)
	publish := routes.Publisher(core)
	publish(policy, "old")
	rt := newXraySelectorRuntime(Options{XrayBinary: "xray", XrayAPIServer: "local"})
	rt.routePublisher = publish
	rt.pool = healthPool{Policies: map[string][]string{policy: {"node"}}, PolicyPrefixes: map[string]string{policy: policy + "-"}, Outbounds: map[string]json.RawMessage{"node": json.RawMessage(`{"protocol":"freedom"}`)}}
	selected := map[string]string{policy: "old"}
	installed := map[string]bool{}
	badReadback := true
	rt.command = func(_ context.Context, _ time.Duration, _ string, args ...string) ([]byte, error) {
		switch args[1] {
		case "bi":
			selector := args[len(args)-1]
			if badReadback && installed[rt.policyRuntimeTag(policy, "node")] && selector == policy {
				return selectorInfo("old"), nil
			}
			return selectorInfo(selected[selector]), nil
		case "ado":
			installed[rt.policyRuntimeTag(policy, "node")] = true
			return nil, nil
		case "lso":
			return outboundTagsJSON(rt.policyRuntimeTag(policy, "node")), nil
		case "bo":
			selected[args[len(args)-2]] = args[len(args)-1]
			return nil, nil
		}
		t.Fatalf("unexpected mutation: %v", args)
		return nil, nil
	}
	read := func() (s dnsroute.Stamp) { routes.Read(policy, func(stamp dnsroute.Stamp) { s = stamp }); return }
	before := read()
	if rt.Select(policy, "node") == nil || read() != before {
		t.Fatal("failed readback published an intended route")
	}
	badReadback = false
	if err := rt.Select(policy, "node"); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after.Tag != rt.policyRuntimeTag(policy, "node") || after.Epoch <= before.Epoch {
		t.Fatal("confirmed immutable handler was not published")
	}
	if _, err := rt.Current(policy); err != nil || read() != after {
		t.Fatal("unchanged readback invalidated route")
	}
	// Isolated probe selections must never become a production DNS route.
	if err := rt.Select(rt.probeSelectorName(), "node"); err != nil {
		t.Fatal(err)
	}
	if read() != after {
		t.Fatal("probe selection changed production DNS")
	}
}
