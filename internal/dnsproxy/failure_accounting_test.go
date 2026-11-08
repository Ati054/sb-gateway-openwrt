package dnsproxy

import (
	"context"
	"net/netip"
	"testing"
)

func TestDNSFailuresExcludeRejectedInputAndKeepOperationalFailures(t *testing.T) {
	s := &server{config: Config{DefaultPolicy: "main", DirectURL: "invalid://endpoint"}}
	for range 200 {
		s.resolve(context.Background(), []byte{1}, netip.MustParseAddr("192.0.2.8"))
	}
	if s.failures.Load() != 200 || s.failureInvalidQuery.Load() != 200 || s.dnsFailures.Load() != 0 {
		t.Fatal("input rejections were counted as failed DNS resolution")
	}
	s.resolve(context.Background(), dnsQuery("valid.example.test"), netip.MustParseAddr("127.0.0.1"))
	if s.failures.Load() != 201 || s.failureInvalidQuery.Load() != 200 || s.dnsFailures.Load() != 1 || s.failureEndpoint.Load() != 1 {
		t.Fatal("a real DNS processing error was hidden")
	}
}
