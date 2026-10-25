package rulesets

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const geoSiteReleaseURL = "https://raw.githubusercontent.com/v2fly/domain-list-community/release/dlc.dat_plain.yml"
const maxGeoSiteReleaseSize = 16 << 20

type ReleaseFetch func(context.Context) (io.ReadCloser, error)

// One bounded download replaces hundreds of per-include HTTP requests. The
// full release is never persisted; only selected country rules enter Snapshot.
func GeoSiteReleaseHTTPFetch(timeout time.Duration, transports ...http.RoundTripper) ReleaseFetch {
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("GeoSite redirect is not allowed")
	}}
	if len(transports) > 0 {
		client.Transport = transports[0]
	}
	return func(ctx context.Context) (io.ReadCloser, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, geoSiteReleaseURL, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("User-Agent", "sb-gateway-openwrt/1")
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("download GeoSite release: %w", err)
		}
		if response.StatusCode != http.StatusOK || response.ContentLength > maxGeoSiteReleaseSize {
			response.Body.Close()
			return nil, errors.New("GeoSite release is unavailable or oversized")
		}
		return response.Body, nil
	}
}

// CompileGeoSiteRelease extracts only requested countries from the reviewed
// upstream's plain release. Its strict shape check fails closed on format
// changes instead of silently publishing an incomplete routing table.
func CompileGeoSiteRelease(body string, selected map[string]bool) (map[string]Rules, error) {
	return CompileGeoSiteReader(strings.NewReader(body), selected)
}

// CompileGeoSiteReader retains only selected country entries, never the full
// download or a second byte-to-string copy. Validate UTF-8 even in skipped lists.
func CompileGeoSiteReader(source io.Reader, selected map[string]bool) (map[string]Rules, error) {
	limited := &io.LimitedReader{R: source, N: maxGeoSiteReleaseSize + 1}
	reader := bufio.NewReader(limited)
	header, err := reader.ReadSlice('\n')
	if err != nil || string(header) != "lists:\n" {
		return nil, errors.New("invalid GeoSite release")
	}

	wanted := make(map[string]string, len(selected))
	for country := range selected {
		root := countryGeositeRoot[country]
		if root == "" {
			return nil, fmt.Errorf("unsupported GeoSite country %q", country)
		}
		wanted[root] = country
	}
	result := make(map[string]Rules, len(selected))
	current, target := "", ""
	expected, observed := -1, 0
	values := map[string]map[string]bool{}
	finish := func() error {
		if target == "" {
			return nil
		}
		if expected < 1 || observed != expected {
			return fmt.Errorf("incomplete GeoSite %s list", target)
		}
		rules := Rules{Exact: sorted(values["exact"]), Suffix: sorted(values["suffix"]),
			Keyword: sorted(values["keyword"]), Regex: sorted(values["regex"])}
		if rules.Count() == 0 {
			return fmt.Errorf("empty GeoSite %s list", target)
		}
		result[target] = rules
		return nil
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 256<<10)
	for scanner.Scan() {
		if !utf8.Valid(scanner.Bytes()) {
			return nil, errors.New("invalid GeoSite UTF-8")
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "  - name: ") {
			if err := finish(); err != nil {
				return nil, err
			}
			name, err := strconv.Unquote(strings.TrimPrefix(line, "  - name: "))
			if err != nil {
				return nil, errors.New("invalid GeoSite list name")
			}
			current, target = name, wanted[name]
			expected, observed = -1, 0
			values = map[string]map[string]bool{"exact": {}, "suffix": {}, "keyword": {}, "regex": {}}
			continue
		}
		if target == "" {
			continue
		}
		if strings.HasPrefix(line, "    length: ") {
			var err error
			expected, err = strconv.Atoi(strings.TrimPrefix(line, "    length: "))
			if err != nil || expected < 1 || expected > maxEntries {
				return nil, fmt.Errorf("invalid GeoSite %s list length", target)
			}
			continue
		}
		if line == "    rules:" {
			continue
		}
		if !strings.HasPrefix(line, "      - ") || current == "" {
			return nil, fmt.Errorf("invalid GeoSite %s list format", target)
		}
		raw, err := strconv.Unquote(strings.TrimPrefix(line, "      - "))
		if err != nil {
			return nil, fmt.Errorf("invalid GeoSite %s entry", target)
		}
		observed++
		if before, _, hasAttributes := strings.Cut(raw, ":@"); hasAttributes {
			raw = before
		}
		kind, value, ok := strings.Cut(raw, ":")
		if !ok || value == "" || len(value) > 1024 {
			return nil, fmt.Errorf("invalid GeoSite %s entry", target)
		}
		switch kind {
		case "domain":
			kind = "suffix"
		case "full":
			kind = "exact"
		case "regexp":
			kind = "regex"
		case "keyword":
		default:
			return nil, fmt.Errorf("unsupported GeoSite %s entry", target)
		}
		if kind == "suffix" || kind == "exact" {
			if !validDomain(value) {
				return nil, fmt.Errorf("invalid GeoSite %s domain", target)
			}
		} else if kind == "regex" {
			if _, err := regexp.Compile(value); err != nil {
				return nil, fmt.Errorf("invalid GeoSite %s regex: %w", target, err)
			}
		}
		values[kind][value] = true
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if limited.N == 0 {
		return nil, errors.New("GeoSite release is oversized")
	}
	if err := finish(); err != nil {
		return nil, err
	}
	if len(result) != len(selected) {
		return nil, errors.New("selected GeoSite country missing from release")
	}
	return result, nil
}
