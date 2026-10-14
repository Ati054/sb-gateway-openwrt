// Package releaseupdate fetches this project's published GitHub APK releases.
package releaseupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const Repository = "Ati054/sb-gateway-openwrt"
const Root = "/var/run/sb-gateway-upgrade"
const Candidate = "/tmp/sb-gateway-upgrade.apk"
const maxAPK int64 = 64 << 20

var versionPattern = regexp.MustCompile(`^[0-9][A-Za-z0-9._+~-]{0,79}$`)
var tagPattern = regexp.MustCompile(`^v[0-9][A-Za-z0-9._-]{0,79}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Asset struct {
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Schema  int     `json:"schema"`
	Version string  `json:"version"`
	Tag     string  `json:"tag"`
	Assets  []Asset `json:"assets"`
}
type Choice struct {
	Version   string    `json:"version"`
	Tag       string    `json:"tag"`
	URL       string    `json:"url"`
	Asset     Asset     `json:"asset"`
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at"`
}
type githubAsset struct {
	Name  string `json:"name"`
	URL   string `json:"browser_download_url"`
	Size  int64  `json:"size"`
	State string `json:"state"`
}
type githubRelease struct {
	Tag         string        `json:"tag_name"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt time.Time     `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}
type Installed struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
}
type Source struct {
	SigningKey ed25519.PublicKey
	Client     *http.Client
	Compare    func(context.Context, string, string) (bool, error)
}

func NewSource() *Source {
	t := &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 30 * time.Second, TLSHandshakeTimeout: 15 * time.Second, DisableCompression: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	return &Source{SigningKey: signingPublicKey(), Client: &http.Client{Transport: t, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !safeURL(r.URL) {
			return errors.New("GitHub download redirect rejected")
		}
		return nil
	}}, Compare: func(ctx context.Context, next, current string) (bool, error) {
		b, e := exec.CommandContext(ctx, "apk", "version", "-t", next, current).Output()
		return strings.TrimSpace(string(b)) == ">", e
	}}
}
func safeURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	switch u.Hostname() {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}
func assetURL(tag, name string) string {
	return "https://github.com/" + Repository + "/releases/download/" + tag + "/" + name
}
func (s *Source) get(ctx context.Context, address string) (*http.Response, error) {
	u, e := url.Parse(address)
	if e != nil || !safeURL(u) {
		return nil, errors.New("Invalid GitHub download URL")
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if e != nil {
		return nil, e
	}
	r.Header.Set("User-Agent", "SB-Gateway-OpenWrt")
	r.Header.Set("Accept", "application/vnd.github+json")
	response, e := s.Client.Do(r)
	if e != nil {
		return nil, errors.New("Cannot reach GitHub; check the router's Internet connection and time")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		if response.StatusCode == 403 || response.StatusCode == 429 {
			return nil, errors.New("GitHub request limit reached; try again later")
		}
		return nil, fmt.Errorf("GitHub returned HTTP %d", response.StatusCode)
	}
	return response, nil
}
func (s *Source) readBlob(ctx context.Context, address string, limit int64) ([]byte, error) {
	response, err := s.get(ctx, address)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, errors.New("GitHub response interrupted")
	}
	if int64(len(body)) > limit {
		return nil, errors.New("GitHub response exceeds the size limit")
	}
	return body, nil
}
func (s *Source) readJSON(ctx context.Context, address string, limit int64, result any) error {
	body, err := s.readBlob(ctx, address, limit)
	if err != nil {
		return err
	}
	if json.Unmarshal(body, result) != nil {
		return errors.New("Invalid GitHub release metadata")
	}
	return nil
}

func (s *Source) Check(ctx context.Context, installed Installed) (Choice, error) {
	if installed.Name != "sb-gateway-openwrt" || !versionPattern.MatchString(installed.Version) {
		return Choice{}, errors.New("Installed package metadata unavailable")
	}
	if installed.Arch != "aarch64_generic" && installed.Arch != "aarch64_cortex-a53" {
		return Choice{}, errors.New("No GitHub APK for this architecture")
	}
	var releases []githubRelease
	if e := s.readJSON(ctx, "https://api.github.com/repos/"+Repository+"/releases?per_page=20", 1<<20, &releases); e != nil {
		return Choice{}, e
	}
	result := Choice{CheckedAt: time.Now().UTC()}
	for _, release := range releases {
		if release.Draft || release.Prerelease || release.PublishedAt.IsZero() || !tagPattern.MatchString(release.Tag) {
			continue
		}
		var metadata, signature *githubAsset
		for i := range release.Assets {
			a := &release.Assets[i]
			if a.Name == "release.json" && a.State == "uploaded" && a.Size > 0 && a.Size <= 16384 && a.URL == assetURL(release.Tag, a.Name) {
				metadata = a
			}
			if a.Name == "release.json.sig" && a.State == "uploaded" && a.Size > 0 && a.Size <= 256 && a.URL == assetURL(release.Tag, a.Name) {
				signature = a
			}
		}
		if metadata == nil {
			continue
		}
		if signature == nil {
			// Releases predating signing cannot be offered for installation.
			// An already installed or older unsigned release is simply history.
			taggedVersion := strings.TrimPrefix(release.Tag, "v")
			if versionPattern.MatchString(taggedVersion) {
				newer, err := s.Compare(ctx, taggedVersion, installed.Version)
				if err != nil {
					return Choice{}, errors.New("Cannot compare package versions")
				}
				if !newer {
					continue
				}
			}
			return Choice{}, errors.New("Release signature is missing")
		}
		body, e := s.readBlob(ctx, metadata.URL, 16384)
		if e != nil {
			return Choice{}, e
		}
		sig, e := s.readBlob(ctx, signature.URL, 256)
		if e != nil {
			return Choice{}, e
		}
		if e = verifyManifest(s.SigningKey, body, sig); e != nil {
			return Choice{}, e
		}
		var manifest Manifest
		if json.Unmarshal(body, &manifest) != nil {
			return Choice{}, errors.New("Invalid signed release manifest")
		}

		if manifest.Schema != 1 || manifest.Tag != release.Tag || !versionPattern.MatchString(manifest.Version) || len(manifest.Assets) > 8 {
			return Choice{}, errors.New("Invalid release manifest")
		}
		newer, e := s.Compare(ctx, manifest.Version, installed.Version)
		if e != nil {
			return Choice{}, errors.New("Cannot compare package versions")
		}
		for _, asset := range manifest.Assets {
			if asset.Arch != installed.Arch {
				continue
			}
			expected := "sb-gateway-openwrt-" + manifest.Version + "-" + installed.Arch + ".apk"
			if asset.Name != expected || asset.Size <= 0 || asset.Size > maxAPK || !shaPattern.MatchString(asset.SHA256) {
				return Choice{}, errors.New("Invalid release APK metadata")
			}
			found := false
			for _, published := range release.Assets {
				if published.Name == asset.Name && published.Size == asset.Size && published.State == "uploaded" && published.URL == assetURL(release.Tag, asset.Name) {
					found = true
				}
			}
			if !found {
				return Choice{}, errors.New("Release APK is missing or incomplete")
			}
			return Choice{Version: manifest.Version, Tag: release.Tag, URL: assetURL(release.Tag, asset.Name), Asset: asset, Available: newer, CheckedAt: time.Now().UTC()}, nil

		}
	}
	return result, nil
}
func (s *Source) Download(ctx context.Context, choice Choice, destination string) error {
	a := choice.Asset
	if !choice.Available || !versionPattern.MatchString(choice.Version) || !tagPattern.MatchString(choice.Tag) || (a.Arch != "aarch64_generic" && a.Arch != "aarch64_cortex-a53") || a.Name != "sb-gateway-openwrt-"+choice.Version+"-"+a.Arch+".apk" || choice.URL != assetURL(choice.Tag, a.Name) || a.Size <= 0 || a.Size > maxAPK || !shaPattern.MatchString(a.SHA256) {
		return errors.New("Update metadata is invalid; check for updates again")
	}
	response, e := s.get(ctx, choice.URL)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.ContentLength >= 0 && response.ContentLength != a.Size {
		return errors.New("APK size differs from the release manifest")
	}
	file, e := os.CreateTemp(filepath.Dir(destination), ".sb-gateway-download-*.apk")
	if e != nil {
		return errors.New("Cannot create APK in temporary storage")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if e = file.Chmod(0600); e != nil {
		return e
	}
	hash := sha256.New()
	n, e := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, a.Size+1))
	if e != nil {
		return errors.New("APK download interrupted; try again")
	}
	if n != a.Size || hex.EncodeToString(hash.Sum(nil)) != a.SHA256 {
		return errors.New("APK checksum or size verification failed")
	}
	if e = file.Close(); e != nil {
		return e
	}
	return os.Rename(file.Name(), destination)
}
func ReadInstalled(ctx context.Context) (Installed, error) {
	b, e := exec.CommandContext(ctx, "apk", "query", "--fields", "name,version,arch", "--format", "json", "--installed", "sb-gateway-openwrt").Output()
	if e != nil {
		return Installed{}, errors.New("Installed package metadata unavailable")
	}
	var list []Installed
	if json.Unmarshal(b, &list) != nil || len(list) != 1 {
		return Installed{}, errors.New("Installed package metadata unavailable")
	}
	return list[0], nil
}
func Save(root string, choice Choice) error {
	if e := os.MkdirAll(root, 0700); e != nil {
		return e
	}
	b, e := json.Marshal(choice)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(root, ".release-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), filepath.Join(root, "release.json"))
}
func CheckWorker(ctx context.Context) error {
	installed, e := ReadInstalled(ctx)
	if e != nil {
		return e
	}
	s := NewSource()
	defer s.Client.CloseIdleConnections()
	choice, e := s.Check(ctx, installed)
	if e != nil {
		return e
	}
	return Save(Root, choice)
}
func DownloadWorker(ctx context.Context) error {
	b, e := os.ReadFile(filepath.Join(Root, "release.json"))
	if e != nil || len(b) > 16384 {
		return errors.New("Check for updates first")
	}
	var choice Choice
	if json.Unmarshal(b, &choice) != nil || !choice.Available || time.Since(choice.CheckedAt) > 15*time.Minute || time.Until(choice.CheckedAt) > time.Minute {
		return errors.New("Update check expired; check for updates again")
	}
	installed, e := ReadInstalled(ctx)
	if e != nil {
		return e
	}
	s := NewSource()
	defer s.Client.CloseIdleConnections()
	fresh, e := s.Check(ctx, installed)
	if e != nil {
		return e
	}
	if !fresh.Available || fresh.Version != choice.Version || fresh.Tag != choice.Tag || fresh.Asset != choice.Asset {
		return errors.New("Release changed; check for updates again")
	}
	// Reserve download space in RAM before touching the running service.
	mem, e := os.ReadFile("/proc/meminfo")
	if e != nil {
		return e
	}
	var available int64
	for _, line := range strings.Split(string(mem), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &available)
		}
	}
	df, e := exec.CommandContext(ctx, "df", "-Pk", "/tmp").Output()
	if e != nil {
		return e
	}
	var free int64
	lines := strings.Split(strings.TrimSpace(string(df)), "\n")
	if len(lines) < 2 {
		return errors.New("Cannot check temporary storage")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return errors.New("Cannot check temporary storage")
	}
	fmt.Sscanf(fields[3], "%d", &free)
	needed := (choice.Asset.Size+1023)/1024 + 32768
	if available < needed || free < needed {
		return errors.New("Not enough RAM for APK download; free temporary storage and retry")
	}
	if e = s.Download(ctx, choice, Candidate); e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(Root, "github-sha256"), []byte(choice.Asset.SHA256+"\n"), 0600)
}
