package daemon

import (
	"reflect"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func TestRefreshDue(t *testing.T) {
	now := time.Unix(1000, 0)
	base := settings.Config{SubscriptionURL: "https://example.test/feed", UpdateMode: "automatic"}
	if !refreshDue(base, now, now, false) || refreshDue(base, now, now.Add(time.Second), false) {
		t.Fatal("automatic refresh schedule is wrong")
	}
	base.UpdateMode = "manual"
	if refreshDue(base, now, now.Add(-time.Hour), false) || !refreshDue(base, now, now, true) {
		t.Fatal("manual mode must refresh only on request")
	}
	base.SubscriptionURL = ""
	if refreshDue(base, now, now, true) {
		t.Fatal("refresh without a subscription URL must not run")
	}
}

func TestIndependentSourceScheduleAndFetchPath(t *testing.T) {
	now := time.Unix(1000, 0)
	source := settings.Subscription{URL: "https://example.test/feed", UpdateMode: "manual"}
	if sourceRefreshDue(source, now, now.Add(-time.Hour), false) || !sourceRefreshDue(source, now, now, true) {
		t.Fatal("manual source scheduled without request or rejected a request")
	}
	for _, example := range []struct {
		mode      string
		available bool
		want      []int
	}{
		{"proxy-first", true, []int{19080, 0}},
		{"proxy-first", false, []int{0}},
		{"direct-first", true, []int{0, 19080}},
		{"proxy-only", false, nil},
		{"proxy-only", true, []int{19080}},
		{"direct-only", true, []int{0}},
	} {
		if got := fetchPorts(example.mode, example.available, 19080); !reflect.DeepEqual(got, example.want) {
			t.Fatalf("fetchPorts(%q, %v) = %v, want %v", example.mode, example.available, got, example.want)
		}
	}
}
