package settings

import "testing"

func TestURLTestPlanningUCISelection(t *testing.T) {
	for _, mode := range []string{"", "fresh-paired", "isolated-paired", "route-paired", "invalid"} {
		c, e := ReadUCI(t.Context(), fake(map[string]string{"resproxy.main.urltest_planning_mode": mode}))
		if mode == "invalid" {
			if e == nil {
				t.Fatal("invalid mode accepted")
			}
			continue
		}
		if e != nil || c.URLTestPlanningMode != mode {
			t.Fatalf("mode=%q got=%q err=%v", mode, c.URLTestPlanningMode, e)
		}
	}
}
