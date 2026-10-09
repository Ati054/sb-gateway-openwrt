package splithttp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/xtls/xray-core/transport/internet"
)

type sbFailedRoundTripper struct{}

func (sbFailedRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("expected synthetic establishment failure")
}

func TestSBConcurrentFailedOpenStream(t *testing.T) {
	client := &DefaultDialerClient{
		transportConfig: &Config{}, owner: &internet.MemoryStreamConfig{},
		client: &http.Client{Transport: sbFailedRoundTripper{}},
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 100 {
				body, _, _, err := client.OpenStream(context.Background(), "http://example.test/", "", nil, false)
				if err == nil || body != nil {
					t.Error("failed establishment returned a stream")
				}
			}
		})
	}
	workers.Wait()
}
