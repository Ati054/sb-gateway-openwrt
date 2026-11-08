package capture

import (
	"strings"
	"testing"
)

func TestProtocolDNSKeepsRouterDNSAndProxiesExternalPort53(t *testing.T) {
	p, _ := New("proxy", []string{"192.168.0.0/16"})
	p.ProtocolDNS = true
	body, err := Render("br-lan", 12345, 1053, p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(body, "dport 53 redirect to :1053") != 4 {
		t.Fatal("external DNS is still unconditionally redirected or router DNS was lost")
	}
	for _, target := range []string{"127.0.0.2:12345", "[::2]:12345"} {
		if !strings.Contains(body, "th dport 53 tproxy to "+target+" meta mark set 0x5253 accept") {
			t.Fatal("missing protocol classification gateway")
		}
	}
	if strings.Contains(body, "udp dport 53 return") || strings.Contains(body, "tcp dport 53 return") {
		t.Fatal("external DNS escaped transparent capture")
	}
	body, err = Render("br-lan", 12345, 0, p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "127.0.0.2") || strings.Contains(body, "[::2]") || strings.Contains(body, "redirect") {
		t.Fatal("system DNS mode used the managed DNS gateway")
	}
}
