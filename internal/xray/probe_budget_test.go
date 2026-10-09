package xray

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenWrtProbeBudgetRendersDistinctLanesAndRefreshPort(t *testing.T) {
	for _, budget := range []int{1, 10, 64} {
		c := DefaultBaseConfig()
		c.ProbeBudget = budget
		c.DNSPolicyBasePort = 19200
		body, err := RenderBase(c)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Inbounds []struct {
				Tag  string
				Port int
			}
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		ports := map[int]bool{}
		lanes := 0
		refresh := 0
		for _, in := range doc.Inbounds {
			if strings.HasPrefix(in.Tag, "outbound-health-") || in.Tag == RefreshProbeTag {
				if ports[in.Port] {
					t.Fatal("overlapping ports")
				}
				ports[in.Port] = true
			}
			for _, tag := range HealthProbeTags(c) {
				if in.Tag == tag {
					lanes++
				}
			}
			if in.Tag == RefreshProbeTag {
				refresh = in.Port
			}
		}
		if lanes != budget+1 || refresh != c.ProbeBasePort+budget+1 {
			t.Fatalf("budget=%d lanes=%d refresh=%d", budget, lanes, refresh)
		}
	}
}
