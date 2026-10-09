package rulesets

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCompileGeoIP(t *testing.T) {
	got, err := CompileGeoIP("# country\n203.0.113.0/24\n2001:db8::/32\n203.0.113.0/24\n")
	if err != nil || !reflect.DeepEqual(got, []string{"2001:db8::/32", "203.0.113.0/24"}) {
		t.Fatalf("GeoIP compile: %v, %v", got, err)
	}
	for _, source := range []string{"0.0.0.0/0", "203.0.113.1/24", "invalid", ""} {
		if _, err := CompileGeoIP(source); err == nil {
			t.Errorf("accepted invalid GeoIP source %q", source)
		}
	}
}

func TestBuildSelectedCountryBothAndStaleFallback(t *testing.T) {
	ctx := context.Background()
	choice := map[string]CountrySelection{"ru": {Geosite: true, GeoIP: true}}
	geoSite := func(context.Context) (string, error) {
		return "lists:\n  - name: \"category-ru\"\n    length: 2\n    rules:\n      - \"domain:ru\"\n      - \"domain:example.ru\"\n", nil
	}
	geoIP := func(_ context.Context, name string) (string, error) {
		if name != "ru" {
			t.Fatalf("unexpected GeoIP source %q", name)
		}
		return "203.0.113.0/24\n", nil
	}
	first, status, err := BuildSelected(ctx, nil, choice, Snapshot{Version: SnapshotVersion}, nil, geoSite, geoIP)
	if err != nil || len(status) != 2 || first.Countries["ru"].Geosite.Count() != 2 ||
		len(first.Countries["ru"].GeoIP) != 1 {
		t.Fatalf("country build: %+v status=%+v err=%v", first, status, err)
	}
	fail := func(context.Context, string) (string, error) { return "", errors.New("WAN down") }
	releaseFail := func(context.Context) (string, error) { return "", errors.New("WAN down") }
	second, status, err := BuildSelected(ctx, nil, choice, first, fail, releaseFail, fail)
	if err != nil || status[0].State != "stale" || status[1].State != "stale" ||
		!reflect.DeepEqual(first.Countries, second.Countries) {
		t.Fatalf("stale country fallback: %+v status=%+v err=%v", second, status, err)
	}
	trimmed, _, err := BuildSelected(ctx, nil, nil, second, fail, releaseFail, fail)
	if err != nil || len(trimmed.Countries) != 0 {
		t.Fatalf("unused country retained: %+v err=%v", trimmed.Countries, err)
	}
}
