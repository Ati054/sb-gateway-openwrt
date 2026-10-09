package subscription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchHTTPSAndConditionalResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"generation-one"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"generation-one"`)
		_, _ = w.Write([]byte(testVLESS))
	}))
	defer server.Close()
	body, etag, unchanged, err := Fetch(context.Background(), server.Client(), server.URL, "")
	if err != nil || unchanged || len(body) == 0 || etag != `"generation-one"` {
		t.Fatalf("first fetch: %d %q %v %v", len(body), etag, unchanged, err)
	}
	_, _, unchanged, err = Fetch(context.Background(), server.Client(), server.URL, etag)
	if err != nil || !unchanged {
		t.Fatalf("conditional fetch: %v %v", unchanged, err)
	}
}

func TestFetchRejectsUnsafeAndLargeFeed(t *testing.T) {
	if _, _, _, err := Fetch(context.Background(), http.DefaultClient, "http://unsafe.test/feed", ""); err == nil {
		t.Fatal("plain HTTP subscription accepted")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", MaxFeedBytes+1)))
	}))
	defer server.Close()
	if _, _, _, err := Fetch(context.Background(), server.Client(), server.URL, ""); err == nil {
		t.Fatal("oversized subscription accepted")
	}
}
