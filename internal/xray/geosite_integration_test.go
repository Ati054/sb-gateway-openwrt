package xray

import (
	"encoding/json"
	"github.com/Ati054/sb-gateway-openwrt/internal/geoipasset"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRealXrayLoadsSharedBinaryGeoSite(t *testing.T) {
	binary := testBinary(t)
	root := t.TempDir()
	ref, err := geoipasset.PublishDomains(root, "cn", []string{"domain:example.cn", "regexp:^example\\.test$"})
	if err != nil {
		t.Fatal(err)
	}
	base := DefaultBaseConfig()
	base.Policies = []BasePolicy{{ID: "a", Rules: []RouteRule{{ID: "geo-a", Action: "direct", CountryDomains: []string{ref}, Country: "cn", UseGeosite: true}}}, {ID: "b", Rules: []RouteRule{{ID: "geo-b", Action: "proxy", CountryDomains: []string{ref}, Country: "cn", UseGeosite: true}}}}
	base.DefaultPolicy = "a"
	body, err := RenderBase(base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "xray.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-test", "-c", path)
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("binary GeoSite rejected: %v\n%s", err, out)
	}
}
