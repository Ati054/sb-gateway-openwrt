package daemon

import (
	"fmt"
	"github.com/Ati054/xroute-openwrt/internal/agentport"
)

// Release packages default to fresh-paired. A private test package can set this
// value through the linker; an explicit UCI choice always takes precedence.
// Changing UCI remains structural and joins the old controller on restart.
var urlTestPlanningDefault = "fresh-paired"

func configuredURLTestPlanningMode(value string) (agentport.URLTestPlanningMode, error) {
	if value == "" {
		value = urlTestPlanningDefault
	}
	switch value {
	case "fresh-paired":
		return agentport.URLTestPlanningFreshPaired, nil
	case "route-paired":
		return agentport.URLTestPlanningRoutePaired, nil
	case "isolated-paired": // Compatibility with the previous private trial.
		return agentport.URLTestPlanningRoutePaired, nil
	default:
		return "", fmt.Errorf("unsupported URLTest planning mode %q", value)
	}
}
