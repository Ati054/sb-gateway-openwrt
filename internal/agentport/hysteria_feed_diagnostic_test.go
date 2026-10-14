//go:build linux

package agentport

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
)

// Compare independent URI fields with persisted values without logging secrets.
func TestRealHysteriaFeedMatchesPersistedFields(t *testing.T) {
	feedPath := os.Getenv("SB_DIAG_HY_FEED")
	if os.Getenv("SB_DIAG_HYSTERIA") != "1" || feedPath == "" {
		t.Skip("opt-in private feed comparison inside QA")
	}
	body, err := os.ReadFile(feedPath)
	if err != nil {
		t.Fatal("private feed unreadable")
	}
	_, nodes, err := (generation.Store{Root: "/etc/resproxy"}).Current()
	if err != nil {
		t.Fatal("private current catalog unreadable")
	}
	checked := 0
	for _, line := range strings.Fields(string(body)) {
		if !strings.HasPrefix(line, "hy2://") && !strings.HasPrefix(line, "hysteria2://") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.User == nil {
			t.Fatal("private Hysteria URI invalid")
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			t.Fatal("private Hysteria port invalid")
		}
		auth := u.User.Username()
		if suffix, ok := u.User.Password(); ok {
			auth += ":" + suffix
		}
		sni := u.Query().Get("sni")
		if sni == "" {
			sni = u.Hostname()
		}
		match := false
		for _, n := range nodes {
			tls, _ := n.Settings["tls"].(map[string]any)
			if n.ProviderID == "Quaattro" && n.Protocol == "hysteria2" && n.Label == u.Fragment && n.Server == u.Hostname() && n.Port == port && n.Credentials["password"] == auth && tls["server_name"] == sni {
				match = true
				break
			}
		}
		if !match {
			t.Fatal("raw feed and catalog endpoint/auth/SNI mismatch")
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no real Hysteria URI compared")
	}
	t.Logf("RAW_FEED_TO_CATALOG nodes=%d endpoint_auth_sni_match=true", checked)
}
