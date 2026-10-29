package releaseupdate

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestX86ReleaseVerifiedAndDownloaded(t *testing.T) {
	body := []byte("x86 APK fixture")
	s, choice, manifest, releases := fixtureSource(t, body)
	choice.Asset.Arch = "x86_64"
	choice.Asset.Name = "sb-gateway-openwrt-" + choice.Version + "-x86_64.apk"
	choice.URL = assetURL(choice.Tag, choice.Asset.Name)
	manifest.Assets[0] = choice.Asset
	(*releases)[0].Assets[2] = githubAsset{Name: choice.Asset.Name, URL: choice.URL, Size: choice.Asset.Size, State: "uploaded"}
	// Preserve the fixture's signed manifest; route only the new APK URL to
	// its original body endpoint.
	original := s.Client.Transport
	s.Client.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == choice.URL {
			clone := r.Clone(r.Context())
			u, _ := url.Parse(assetURL(choice.Tag, "sb-gateway-openwrt-"+choice.Version+"-aarch64_generic.apk"))
			clone.URL = u
			return original.RoundTrip(clone)
		}
		return original.RoundTrip(r)
	})
	got, err := s.Check(context.Background(), Installed{Name: "sb-gateway-openwrt", Version: "0.1.0", Arch: "x86_64"})
	if err != nil || !got.Available || got.Asset.Arch != "x86_64" {
		t.Fatalf("x86 update unavailable: %+v %v", got, err)
	}
	path := filepath.Join(t.TempDir(), "candidate.apk")
	if err := s.Download(context.Background(), got, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(body) {
		t.Fatal("x86 download not preserved")
	}
	if _, err := s.Check(context.Background(), Installed{Name: "sb-gateway-openwrt", Version: "0.1.0", Arch: "x86_64-other"}); err == nil {
		t.Fatal("unknown architecture accepted")
	}
}
