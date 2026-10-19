package dnsproxy

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"testing"
)

func TestUpstreamFailureDiagnosticsSeparateTimeoutAndHTTPRejection(t *testing.T) {
	for _, scenario := range []string{"timeout", "http", "connection"} {
		t.Run(scenario, func(t *testing.T) {
			client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				switch scenario {
				case "timeout":
					return nil, context.DeadlineExceeded
				case "connection":
					return nil, io.EOF
				default:
					return &http.Response{StatusCode: 429, Header: http.Header{}, Body: http.NoBody}, nil
				}
			})}
			s := &server{direct: client, config: Config{DefaultPolicy: "test", DirectURL: "https://resolver.example/dns-query"}}
			response := s.resolve(context.Background(), dnsQuery("private.example"), netip.MustParseAddr("127.0.0.1"))
			if response[3]&15 != 2 || s.failures.Load() != 1 {
				t.Fatal("upstream failure was not returned")
			}
			if (s.upstreamTimeouts.Load() == 1) != (scenario == "timeout") ||
				(s.upstreamInvalid.Load() == 1) != (scenario == "http") ||
				(s.upstreamTransport.Load() == 1) != (scenario == "connection") {
				t.Fatal("wrong diagnostic class")
			}
			if scenario == "http" && s.upstreamHTTPStatus.Load() != 429 {
				t.Fatal("HTTP rejection status missing")
			}
		})
	}
}
