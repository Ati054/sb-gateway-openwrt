package rulesets

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
)

// CountrySelection requests only the assets actually referenced by a route.
type CountrySelection struct {
	Geosite bool
	GeoIP   bool
}

type CountryRules struct {
	Geosite Rules    `json:"geosite,omitempty"`
	GeoIP   []string `json:"geoip,omitempty"`
}

var countryGeositeRoot = map[string]string{
	"ru": "category-ru", "cn": "cn", "ir": "category-ir",
}

const geoIPBase = "https://raw.githubusercontent.com/v2fly/geoip/release/text/"
const maxGeoIPEntries = 50000

// GeoIPHTTPFetch is restricted to three reviewed country files on one host.
func GeoIPHTTPFetch(timeout time.Duration) Fetch {
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("GeoIP redirect is not allowed")
	}}
	return func(ctx context.Context, country string) (string, error) {
		if countryGeositeRoot[country] == "" {
			return "", errors.New("unsupported GeoIP country")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, geoIPBase+url.PathEscape(country)+".txt", nil)
		if err != nil {
			return "", err
		}
		request.Header.Set("Accept", "text/plain")
		request.Header.Set("User-Agent", "sb-gateway-openwrt/1")
		response, err := client.Do(request)
		if err != nil {
			return "", fmt.Errorf("download GeoIP %s: %w", country, err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK || response.ContentLength > maxSourceSize {
			return "", fmt.Errorf("GeoIP %s is unavailable or oversized", country)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, maxSourceSize+1))
		if err != nil || len(body) > maxSourceSize {
			return "", fmt.Errorf("GeoIP %s is invalid or oversized", country)
		}
		return string(body), nil
	}
}

func CompileGeoIP(body string) ([]string, error) {
	if len(body) > maxSourceSize {
		return nil, errors.New("GeoIP source is oversized")
	}
	values := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		value := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Bits() == 0 || prefix.Masked() != prefix {
			return nil, fmt.Errorf("invalid GeoIP CIDR %q", value)
		}
		values[prefix.String()] = true
		if len(values) > maxGeoIPEntries {
			return nil, errors.New("GeoIP country has too many CIDRs")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errors.New("GeoIP country is empty")
	}
	return sorted(values), nil
}

func GeoSiteExpressions(rules Rules) []string {
	if rules.Reference != "" {
		return []string{rules.Reference}
	}
	if rules.Count() == 0 {
		return nil
	}
	values := make([]string, 0, rules.Count())
	for _, group := range []struct {
		prefix string
		items  []string
	}{
		{"full:", rules.Exact}, {"domain:", rules.Suffix},
		{"keyword:", rules.Keyword}, {"regexp:", rules.Regex},
	} {
		for _, item := range group.items {
			values = append(values, group.prefix+item)
		}
	}
	return values
}

// BuildSelected fetches only selected GeoSite/GeoIP country data. If a source
// fails, the last good copy remains active; unrelated routes are unaffected.
func BuildSelected(ctx context.Context, packs []string, countries map[string]CountrySelection,
	previous Snapshot, packFetch Fetch, releaseFetch ReleaseFetch, geoIPFetch Fetch) (Snapshot, []PackStatus, error) {
	result, statuses, err := Build(ctx, packs, previous, packFetch)
	if err != nil {
		return Snapshot{}, nil, err
	}
	result.Countries = make(map[string]CountryRules)
	keys := make([]string, 0, len(countries))
	for country := range countries {
		if countryGeositeRoot[country] == "" {
			return Snapshot{}, nil, fmt.Errorf("unsupported country %q", country)
		}
		keys = append(keys, country)
	}
	sort.Strings(keys)
	selectedGeoSite := map[string]bool{}
	for _, country := range keys {
		if countries[country].Geosite {
			selectedGeoSite[country] = true
		}
	}
	var geoSiteRules map[string]Rules
	var releaseErr error
	if len(selectedGeoSite) > 0 {
		if releaseFetch == nil {
			releaseErr = errors.New("GeoSite release fetcher is unavailable")
		} else {
			var body io.ReadCloser
			body, releaseErr = releaseFetch(ctx)
			if releaseErr == nil {
				if body == nil {
					releaseErr = errors.New("GeoSite body is missing")
				} else {
					geoSiteRules, releaseErr = CompileGeoSiteReader(body, selectedGeoSite)
					if closeErr := body.Close(); releaseErr == nil {
						releaseErr = closeErr
					}
				}
			}
		}
	}
	for _, country := range keys {
		requested := countries[country]
		old := previous.Countries[country]
		compiled := CountryRules{}
		if requested.Geosite {
			rules, fetchErr := geoSiteRules[country], releaseErr
			status := PackStatus{ID: "geosite:" + country, State: "updated"}
			if fetchErr != nil {
				rules = old.Geosite
				status.State, status.Error = "stale", fetchErr.Error()
			} else if sameRules(old.Geosite, rules) {
				status.State = "unchanged"
			}
			compiled.Geosite = rules
			status.Entries = rules.Count()
			statuses = append(statuses, status)
		}
		if requested.GeoIP {
			body, fetchErr := geoIPFetch(ctx, country)
			var cidrs []string
			if fetchErr == nil {
				cidrs, fetchErr = CompileGeoIP(body)
			}
			status := PackStatus{ID: "geoip:" + country, State: "updated"}
			if fetchErr != nil {
				cidrs = old.GeoIP
				status.State, status.Error = "stale", fetchErr.Error()
			} else if strings.Join(old.GeoIP, "\n") == strings.Join(cidrs, "\n") {
				status.State = "unchanged"
			}
			compiled.GeoIP = cidrs
			status.Entries = len(cidrs)
			statuses = append(statuses, status)
		}
		result.Countries[country] = compiled
	}
	if err := result.Validate(); err != nil {
		return Snapshot{}, nil, err
	}
	return result, statuses, nil
}
