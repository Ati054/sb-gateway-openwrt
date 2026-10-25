package rulesets

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type routeTransport func(*http.Request) (*http.Response, error)

func (f routeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEveryRulesetDownloadUsesSelectedTransportAndWANFallback(t *testing.T) {
	for _, kind := range []string{"services", "geoip", "geosite"} {
		t.Run(kind, func(t *testing.T) {
			var calls []string
			makeRoute := func(name string, fail bool) http.RoundTripper {
				return routeTransport(func(r *http.Request) (*http.Response, error) {
					calls = append(calls, name)
					if r.URL.Scheme != "https" || r.URL.Host != "raw.githubusercontent.com" {
						t.Fatal("download escaped pinned HTTPS host", r.URL)
					}
					if fail {
						return nil, errors.New("route unavailable")
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("fixture")), Header: make(http.Header)}, nil
				})
			}
			for _, proxyFails := range []bool{false, true} {
				calls = nil
				proxy, direct := makeRoute("proxy", proxyFails), makeRoute("wan", false)
				var err error
				switch kind {
				case "services":
					_, err = FallbackFetch(HTTPFetch(time.Second, proxy), HTTPFetch(time.Second, direct))(context.Background(), "youtube")
				case "geoip":
					_, err = FallbackFetch(GeoIPHTTPFetch(time.Second, proxy), GeoIPHTTPFetch(time.Second, direct))(context.Background(), "cn")
				case "geosite":
					var body io.ReadCloser
					body, err = GeoSiteReleaseHTTPFetch(time.Second, proxy)(context.Background())
					if err != nil {
						body, err = GeoSiteReleaseHTTPFetch(time.Second, direct)(context.Background())
					}
					if body != nil {
						body.Close()
					}
				}
				want := []string{"proxy"}
				if proxyFails {
					want = append(want, "wan")
				}
				if err != nil || !reflect.DeepEqual(calls, want) {
					t.Fatal("wrong download route", calls, err)
				}
			}
		})
	}
}

func TestRulesetTransportCannotOverrideRedirectGuard(t *testing.T) {
	transport := routeTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "raw.githubusercontent.com" {
			t.Fatal("redirect reached unreviewed host", r.URL)
		}
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://unreviewed.example/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := HTTPFetch(time.Second, transport)(context.Background(), "youtube"); err == nil {
		t.Fatal("service redirect accepted")
	}
	if _, err := GeoIPHTTPFetch(time.Second, transport)(context.Background(), "ru"); err == nil {
		t.Fatal("GeoIP redirect accepted")
	}
	if _, err := GeoSiteReleaseHTTPFetch(time.Second, transport)(context.Background()); err == nil {
		t.Fatal("GeoSite redirect accepted")
	}
}

func TestCanceledRulesetDownloadDoesNotRetryWAN(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	primary := func(context.Context, string) (string, error) { cancel(); return "", context.Canceled }
	fallback := func(context.Context, string) (string, error) { t.Fatal("WAN request after cancel"); return "", nil }
	if _, err := FallbackFetch(primary, fallback)(ctx, "youtube"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestGeoSitePartialStreamRetriesAllSelectedCountries(t *testing.T) {
	var calls []string
	good := "lists:\n  - name: \"category-ru\"\n    length: 1\n    rules:\n      - \"domain:good.ru\"\n  - name: \"cn\"\n    length: 1\n    rules:\n      - \"domain:good.cn\"\n  - name: \"category-ir\"\n    length: 1\n    rules:\n      - \"domain:good.ir\"\n"
	broken := strings.Replace(good, "length: 1", "length: 2", 1)
	source := func(name, text string) ReleaseFetch {
		return func(context.Context) (io.ReadCloser, error) {
			calls = append(calls, name)
			return io.NopCloser(strings.NewReader(text)), nil
		}
	}
	selected := map[string]CountrySelection{"ru": {Geosite: true}, "cn": {Geosite: true}, "ir": {Geosite: true}}
	result, statuses, err := BuildSelected(context.Background(), nil, selected, Snapshot{Version: SnapshotVersion}, nil, source("proxy", broken), nil, source("wan", good))
	if err != nil || !reflect.DeepEqual(calls, []string{"proxy", "wan"}) || len(statuses) != 3 {
		t.Fatal(calls, statuses, err)
	}
	for country := range selected {
		if !reflect.DeepEqual(result.Countries[country].Geosite.Suffix, []string{"good." + country}) {
			t.Fatal("partial or wrong country data published", country, result.Countries)
		}
	}
	calls = nil
	again, statuses, err := BuildSelected(context.Background(), nil, selected, result, nil, source("proxy", broken), nil, source("wan", broken))
	if err != nil || !reflect.DeepEqual(result.Countries, again.Countries) {
		t.Fatal("failed routes replaced last good country data", err)
	}
	for _, status := range statuses {
		if status.State != "stale" || status.Error == "" {
			t.Fatal("missing failed download status", status)
		}
	}
}
