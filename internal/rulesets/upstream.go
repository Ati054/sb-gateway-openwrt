package rulesets

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	upstreamBase  = "https://raw.githubusercontent.com/v2fly/domain-list-community/master/data/"
	maxSourceSize = 4 << 20
	maxIncludes   = 96
	maxTotalSize  = 32 << 20
	maxEntries    = 250000
)

var upstreamName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

// Rules contains domain-list-community matches in the form Xray understands.
// It is built off to the side; callers must publish it only after the complete
// routing transaction has passed validation.
type Rules struct {
	Reference string   `json:"reference,omitempty"`
	Entries   int      `json:"entries,omitempty"`
	Exact     []string `json:"exact,omitempty"`
	Suffix    []string `json:"suffix,omitempty"`
	Keyword   []string `json:"keyword,omitempty"`
	Regex     []string `json:"regex,omitempty"`
}

type Fetch func(context.Context, string) (string, error)

// HTTPFetch only reads the pinned upstream host. Redirects, oversized bodies,
// invalid UTF-8 and non-200 replies fail closed; the last good rules stay put.
func HTTPFetch(timeout time.Duration, transports ...http.RoundTripper) Fetch {
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("ruleset redirect is not allowed")
	}}
	if len(transports) > 0 {
		client.Transport = transports[0]
	}
	return func(ctx context.Context, name string) (string, error) {
		if !upstreamName.MatchString(name) {
			return "", errors.New("invalid upstream ruleset name")
		}
		target := upstreamBase + url.PathEscape(name)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return "", err
		}
		request.Header.Set("Accept", "text/plain")
		request.Header.Set("User-Agent", "resproxy-rulesets/1")
		response, err := client.Do(request)
		if err != nil {
			return "", fmt.Errorf("download %s: %w", name, err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK || response.ContentLength > maxSourceSize {
			return "", fmt.Errorf("ruleset %s is unavailable or oversized", name)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, maxSourceSize+1))
		if err != nil || len(body) > maxSourceSize || !utf8.Valid(body) {
			return "", fmt.Errorf("ruleset %s is invalid or oversized", name)
		}
		return string(body), nil
	}
}

// FallbackFetch retries a failed download through the alternate route. A
// canceled update never starts another network request.
func FallbackFetch(primary, fallback Fetch) Fetch {
	return func(ctx context.Context, name string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		body, err := primary(ctx, name)
		if err == nil || ctx.Err() != nil {
			return body, err
		}
		return fallback(ctx, name)
	}
}

// Compile follows bounded include graphs and accepts the four documented
// domain-list-community match kinds. Attribute suffixes after whitespace are
// intentionally ignored, matching the source project's rule compiler.
func Compile(ctx context.Context, root string, fetch Fetch) (Rules, error) {
	if !upstreamName.MatchString(root) || fetch == nil {
		return Rules{}, errors.New("invalid ruleset source")
	}
	values := map[string]map[string]bool{
		"exact": {}, "suffix": {}, "keyword": {}, "regex": {},
	}
	visited := map[string]bool{}
	count := 0
	totalSize := 0
	var visit func(string) error
	visit = func(name string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if visited[name] {
			return nil
		}
		if !upstreamName.MatchString(name) || len(visited) >= maxIncludes {
			return errors.New("ruleset include graph is invalid or too large")
		}
		visited[name] = true
		body, err := fetch(ctx, name)
		if err != nil {
			return err
		}
		totalSize += len(body)
		if len(body) > maxSourceSize || totalSize > maxTotalSize || !utf8.ValidString(body) {
			return errors.New("ruleset source is invalid or oversized")
		}
		scanner := bufio.NewScanner(strings.NewReader(body))
		scanner.Buffer(make([]byte, 64<<10), maxSourceSize)
		for scanner.Scan() {
			line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
			if line == "" {
				continue
			}
			token := strings.Fields(line)[0]
			if strings.HasPrefix(token, "include:") {
				if err := visit(strings.TrimPrefix(token, "include:")); err != nil {
					return err
				}
				continue
			}
			kind, value, hasPrefix := strings.Cut(token, ":")
			if !hasPrefix {
				kind, value = "suffix", kind
			} else {
				switch kind {
				case "full":
					kind = "exact"
				case "domain":
					kind = "suffix"
				case "regexp":
					kind = "regex"
				case "keyword":
				default:
					return fmt.Errorf("unsupported ruleset match %q", kind)
				}
			}
			if kind != "regex" {
				value = strings.ToLower(value)
			}
			if value == "" || len(value) > 1024 {
				return errors.New("invalid ruleset value")
			}
			if kind == "exact" || kind == "suffix" {
				if !validDomain(value) {
					return fmt.Errorf("invalid ruleset domain %q", value)
				}
			}
			if kind == "regex" {
				if _, err := regexp.Compile(value); err != nil {
					return fmt.Errorf("invalid ruleset regex: %w", err)
				}
			}
			if !values[kind][value] {
				values[kind][value] = true
				count++
				if count > maxEntries {
					return errors.New("ruleset has too many entries")
				}
			}
		}
		return scanner.Err()
	}
	if err := visit(root); err != nil {
		return Rules{}, err
	}
	result := Rules{Exact: sorted(values["exact"]), Suffix: sorted(values["suffix"]),
		Keyword: sorted(values["keyword"]), Regex: sorted(values["regex"])}
	if result.Count() == 0 {
		return Rules{}, errors.New("ruleset has no supported entries")
	}
	return result, nil
}

func (r Rules) Count() int {
	if r.Reference != "" {
		return r.Entries
	}
	return len(r.Exact) + len(r.Suffix) + len(r.Keyword) + len(r.Regex)
}

func sorted(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validDomain(value string) bool {
	if value == "" || len(value) > 253 || strings.Trim(value, ".") != value {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
		}
	}
	return true
}
