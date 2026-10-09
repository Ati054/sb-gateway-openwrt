package subscription

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const MaxFeedBytes = 8 << 20

// NewHTTPClient uses the current Xray HTTP inbound when port is nonzero.
// Bootstrap (no current route) uses a direct client. No insecure TLS mode is
// offered for subscription secrets.
func NewHTTPClient(proxyPort int) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 2
	transport.IdleConnTimeout = 30 * time.Second
	if proxyPort > 0 {
		proxy := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(proxyPort)}
		transport.Proxy = http.ProxyURL(proxy)
	} else {
		transport.Proxy = nil
	}
	return &http.Client{
		Timeout: 30 * time.Second, Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 || request.URL.Scheme != "https" || request.URL.User != nil {
				return errors.New("unsafe subscription redirect")
			}
			return nil
		},
	}
}

func Fetch(ctx context.Context, client *http.Client, rawURL, etag string) ([]byte, string, bool, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, "", false, errors.New("invalid HTTPS subscription URL")
	}
	if client == nil {
		return nil, "", false, errors.New("HTTP client is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", false, errors.New("invalid subscription request")
	}
	request.Header.Set("Accept", "text/plain, application/octet-stream, */*")
	request.Header.Set("User-Agent", "resproxyd/0.1")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", false, errors.New("subscription HTTPS request failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, "", false, errors.New("subscription server did not return HTTP 200")
	}
	if response.ContentLength > MaxFeedBytes {
		return nil, "", false, errors.New("subscription exceeds size limit")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxFeedBytes+1))
	if err != nil || len(body) > MaxFeedBytes {
		return nil, "", false, errors.New("subscription body is incomplete or too large")
	}
	if len(body) == 0 {
		return nil, "", false, errors.New("subscription body is empty")
	}
	return body, response.Header.Get("ETag"), false, nil
}
