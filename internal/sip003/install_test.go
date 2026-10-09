package sip003

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPinnedPackageRangeDownloadAndChecksum(t *testing.T) {
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	noise, _ := z.CreateHeader(&zip.FileHeader{Name: "unneeded.apk", Method: zip.Store})
	noise.Write(bytes.Repeat([]byte{43}, 2<<20))
	payload := bytes.Repeat([]byte("plugin-fixture"), 8192)
	file, _ := z.Create("plugin.apk")
	file.Write(payload)
	z.Close()
	body := archive.Bytes()
	total, requests := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		if start < 0 || end >= len(body) || end-start > 256<<10 {
			t.Error("unbounded request")
			w.WriteHeader(400)
			return
		}
		total += end - start + 1
		requests++
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.WriteHeader(206)
		w.Write(body[start : end+1])
	}))
	defer server.Close()
	pin := packagePin{"plugin.apk", int64(len(payload)), fmt.Sprintf("%x", sha256.Sum256(payload))}
	reader := &rangeReader{ctx: context.Background(), client: server.Client(), url: server.URL, size: int64(len(body)), start: -1}
	var destination bytes.Buffer
	if err := extractPackage(reader, int64(len(body)), pin, &destination); err != nil || !bytes.Equal(destination.Bytes(), payload) {
		t.Fatalf("download: %v", err)
	}
	if total > 512<<10 || requests > 2 {
		t.Fatalf("downloaded irrelevant bundle: %d bytes, %d requests", total, requests)
	}
	pin.SHA = strings.Repeat("0", 64)
	if err := extractPackage(bytes.NewReader(body), int64(len(body)), pin, io.Discard); err == nil {
		t.Fatal("tampered package accepted")
	}
}
func TestRejectUnboundedHTTPResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); w.Write(make([]byte, 300)) }))
	defer server.Close()
	reader := &rangeReader{ctx: context.Background(), client: server.Client(), url: server.URL, size: 300, start: -1}
	if _, err := reader.ReadAt(make([]byte, 3), 0); err == nil {
		t.Fatal("whole bundle response accepted")
	}
}
