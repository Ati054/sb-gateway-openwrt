package releaseupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureSource(t *testing.T, body []byte) (*Source, Choice, *Manifest, *[]githubRelease) {
	t.Helper()
	sum := sha256.Sum256(body)
	choice := Choice{Version: "0.1.0-r85", Tag: "v0.1.0-rc.1", Available: true, CheckedAt: time.Now().UTC(), Asset: Asset{Arch: "aarch64_generic", Name: "sb-gateway-openwrt-0.1.0-r85-aarch64_generic.apk", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}}
	choice.URL = assetURL(choice.Tag, choice.Asset.Name)
	manifest := &Manifest{Schema: 1, Version: choice.Version, Tag: choice.Tag, Assets: []Asset{choice.Asset}}
	releases := []githubRelease{{Tag: choice.Tag, PublishedAt: time.Now().UTC(), Assets: []githubAsset{{Name: "release.json", URL: assetURL(choice.Tag, "release.json"), State: "uploaded", Size: 500}, {Name: choice.Asset.Name, URL: choice.URL, State: "uploaded", Size: choice.Asset.Size}}}}
	s := NewSource()
	s.Compare = func(_ context.Context, a, b string) (bool, error) { return a != b && a == choice.Version, nil }
	s.Client.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		var payload []byte
		switch r.URL.String() {
		case "https://api.github.com/repos/" + Repository + "/releases?per_page=20":
			payload, _ = json.Marshal(releases)
		case assetURL(choice.Tag, "release.json"):
			payload, _ = json.Marshal(manifest)
		case choice.URL:
			payload = body
		default:
			t.Fatalf("Unexpected request %s", r.URL.String())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload)), Header: make(http.Header)}, nil
	})
	return s, choice, manifest, &releases
}
func TestFirstPrereleaseAndCurrentVersion(t *testing.T) {
	s, choice, _, _ := fixtureSource(t, []byte("fixture APK"))
	installed := Installed{Name: "sb-gateway-openwrt", Version: "0.1.0-r84", Arch: "aarch64_generic"}
	got, e := s.Check(context.Background(), installed)
	if e != nil || !got.Available || got.Asset != choice.Asset {
		t.Fatalf("prerelease not offered: %+v %v", got, e)
	}
	installed.Version = choice.Version
	got, e = s.Check(context.Background(), installed)
	if e != nil || got.Available || got.Tag != choice.Tag {
		t.Fatalf("current version offered again: %+v %v", got, e)
	}
}
func TestInvalidOrMissingReleaseCannotBeInstalled(t *testing.T) {
	for _, test := range []string{"draft", "missing-apk", "other-repository", "wrong-size", "path-traversal", "bad-hash", "wrong-schema", "other-tag", "unsupported-arch"} {
		t.Run(test, func(t *testing.T) {
			s, _, m, releases := fixtureSource(t, []byte("fixture APK"))
			installed := Installed{Name: "sb-gateway-openwrt", Version: "0.1.0-r84", Arch: "aarch64_generic"}
			switch test {
			case "draft":
				(*releases)[0].Draft = true
			case "missing-apk":
				(*releases)[0].Assets = (*releases)[0].Assets[:1]
			case "other-repository":
				(*releases)[0].Assets[1].URL = "https://github.com/other/repo/evil.apk"
			case "wrong-size":
				(*releases)[0].Assets[1].Size++
			case "path-traversal":
				m.Assets[0].Name = "../payload.apk"
			case "bad-hash":
				m.Assets[0].SHA256 = "invalid"
			case "wrong-schema":
				m.Schema = 99
			case "other-tag":
				m.Tag = "v9.0.0"
			case "unsupported-arch":
				installed.Arch = "arm_cortex-a7"
			}
			got, e := s.Check(context.Background(), installed)
			if got.Available || (e == nil && test != "draft") {
				t.Fatalf("unsafe release accepted: %+v %v", got, e)
			}
		})
	}
}
func TestDownloadVerifiedAndAtomic(t *testing.T) {
	data := []byte("fixture APK")
	s, choice, _, _ := fixtureSource(t, data)
	path := filepath.Join(t.TempDir(), "candidate.apk")
	if e := s.Download(context.Background(), choice, path); e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, data) {
		t.Fatal("wrong payload")
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("credentials-readable download")
	}
	choice.Asset.SHA256 = strings.Repeat("0", 64)
	if e := s.Download(context.Background(), choice, path); e == nil {
		t.Fatal("checksum mismatch accepted")
	}
	got, _ = os.ReadFile(path)
	if !bytes.Equal(got, data) {
		t.Fatal("failed download replaced existing payload")
	}
	files, _ := os.ReadDir(filepath.Dir(path))
	if len(files) != 1 {
		t.Fatal("partial downloads leaked")
	}
}
func TestDownloadRejectsWrongOrTruncatedSize(t *testing.T) {
	for _, kind := range []string{"short", "oversized", "interrupted"} {
		t.Run(kind, func(t *testing.T) {
			s, choice, _, _ := fixtureSource(t, []byte("fixture APK"))
			path := filepath.Join(t.TempDir(), "candidate.apk")
			s.Client.Transport = fixtureTransport(func(*http.Request) (*http.Response, error) {
				var body io.Reader = strings.NewReader("short")
				if kind == "oversized" {
					body = strings.NewReader(strings.Repeat("x", 100))
				}
				if kind == "interrupted" {
					body = errorReader{}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(body), ContentLength: -1}, nil
			})
			if e := s.Download(context.Background(), choice, path); e == nil {
				t.Fatal("invalid transfer accepted")
			}
			if _, e := os.Stat(path); !os.IsNotExist(e) {
				t.Fatal("failed payload retained")
			}
			files, _ := os.ReadDir(filepath.Dir(path))
			if len(files) != 0 {
				t.Fatal("temporary APK retained")
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("fixture disconnect") }
func TestNetworkLimitsAndRedirectValidation(t *testing.T) {
	for _, address := range []string{"http://github.com/file", "https://github.com:8443/file", "https://user@github.com/file", "https://attacker.example/file", "https://github.com/file#fragment"} {
		u, _ := url.Parse(address)
		if safeURL(u) {
			t.Fatal("unsafe URL accepted")
		}
	}
	for _, address := range []string{"https://github.com/file", "https://release-assets.githubusercontent.com/file?token=public-signed-url", "https://objects.githubusercontent.com/file"} {
		u, _ := url.Parse(address)
		if !safeURL(u) {
			t.Fatal("GitHub CDN rejected")
		}
	}
	s, _, _, _ := fixtureSource(t, []byte("APK"))
	s.Client.Transport = fixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("quota"))}, nil
	})
	if _, e := s.Check(context.Background(), Installed{Name: "sb-gateway-openwrt", Version: "0.1.0-r84", Arch: "aarch64_generic"}); e == nil || !strings.Contains(e.Error(), "limit") {
		t.Fatalf("quota not surfaced: %v", e)
	}
	s.Client.Transport = fixtureTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 16385)))}, nil
	})
	if e := s.readJSON(context.Background(), assetURL("v1", "release.json"), 16384, &Manifest{}); e == nil {
		t.Fatal("oversized metadata accepted")
	}
}

func TestUpdateSkipsIntermediateVersions(t *testing.T) {
	for _, current := range []string{"0.0.9-r1", "0.1.0-r1", "0.1.0-r11", "0.1.0-r79", "0.1.0-r84"} {
		t.Run(current, func(t *testing.T) {
			s, choice, _, _ := fixtureSource(t, []byte("APK"))
			got, err := s.Check(context.Background(), Installed{Name: "sb-gateway-openwrt", Version: current, Arch: "aarch64_generic"})
			if err != nil || !got.Available || got.Version != choice.Version || got.Tag != choice.Tag {
				t.Fatalf("intermediate update required: %+v %v", got, err)
			}
		})
	}
}
