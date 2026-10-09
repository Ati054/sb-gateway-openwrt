package sip003

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const installRoot = "/tmp/resproxy-plugins"
const releaseURL = "https://github.com/Openwrt-Passwall/openwrt-passwall2/releases/download/26.10.1-2/"
const archiveURL = releaseURL + "packages_apk_aarch64_generic.zip"
const archiveSize int64 = 23446312

type packagePin struct {
	File string
	Size int64
	SHA  string
}

var pins = map[string]packagePin{
	"v2ray-plugin": {"v2ray-plugin-5.49.0-r1.apk", 6165926, "560e73239aedf6ee514ba34f8b7ffd29ecbd278f933db591328fc263e29d9500"},
	"obfs-local":   {"simple-obfs-client-0.0.5-r3.apk", 31894, "d5c262ef2e8b8383aa9311dcae02d3ae46d1eb11cb8e088276d542094bf0df74"},
}

type InstallState struct {
	Name  string `json:"name,omitempty"`
	Phase string `json:"phase,omitempty"`
	Error string `json:"error,omitempty"`
	PID   int    `json:"pid,omitempty"`
}
type PluginStatus struct {
	Installed map[string]bool `json:"installed"`
	Supported bool            `json:"supported"`
	Source    string          `json:"source"`
	State     InstallState    `json:"state"`
}

func Status() PluginStatus {
	result := PluginStatus{Installed: map[string]bool{}, Supported: runtime.GOOS == "linux" && runtime.GOARCH == "arm64", Source: "Passwall · 26.10.1-2"}
	for name := range pins {
		info, err := os.Stat(Binary(name))
		result.Installed[name] = err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
	}
	if body, err := os.ReadFile(filepath.Join(installRoot, "status.json")); err == nil && len(body) <= 4096 {
		_ = json.Unmarshal(body, &result.State)
	}
	if result.State.Phase != "success" && result.State.Phase != "error" && result.State.Phase != "" && result.State.PID > 0 {
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(result.State.PID))); err != nil {
			result.State.Phase = "error"
			result.State.Error = "installer stopped"
			_ = os.Remove(filepath.Join(installRoot, "install.lock"))
		}
	}
	return result
}
func writeInstall(state InstallState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(installRoot, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(installRoot, ".status-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err = temporary.Write(body); err != nil {
		temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(installRoot, "status.json"))
}

// Start a fixed worker immediately. LuCI need not hold an RPC during download.
func BeginInstall(name string) error {
	if _, ok := pins[name]; !ok {
		return errors.New("unknown plugin")
	}
	status := Status()
	if !status.Supported {
		return errors.New("unsupported router architecture")
	}
	if status.Installed[name] {
		return nil
	}
	if err := os.MkdirAll(installRoot, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(installRoot, "install.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("plugin installation is already running")
	}
	lock.Close()
	failed := true
	defer func() {
		if failed {
			_ = os.Remove(filepath.Join(installRoot, "install.lock"))
		}
	}()
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	child := exec.Command(binary, "plugins-worker", name)
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer null.Close()
	child.Stdin = null
	child.Stdout = null
	child.Stderr = null
	if err = writeInstall(InstallState{Name: name, Phase: "queued", PID: os.Getpid()}); err != nil {
		return err
	}
	if err = child.Start(); err != nil {
		return err
	}
	// Worker writes its own PID before downloading. Do not overwrite that state.
	failed = false
	if err = writeInstall(InstallState{Name: name, Phase: "queued", PID: child.Process.Pid}); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		failed = true
		return err
	}
	_ = child.Process.Release()
	return nil
}
func InstallWorker(name string) (result error) {
	pin, ok := pins[name]
	if !ok {
		return errors.New("unknown plugin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	state := InstallState{Name: name, PID: os.Getpid()}
	phase := func(value string) { state.Phase = value; _ = writeInstall(state) }
	defer func() {
		if result != nil {
			state.Phase = "error"
			state.Error = result.Error()
		} else {
			state.Phase = "success"
		}
		_ = writeInstall(state)
		_ = os.Remove(filepath.Join(installRoot, "install.lock"))
	}()
	// Parent publishes our PID before releasing the worker.
	deadline := time.Now().Add(5 * time.Second)
	for Status().State.PID != os.Getpid() {
		if time.Now().After(deadline) {
			return errors.New("installer startup failed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	phase("downloading")
	file, err := os.CreateTemp(installRoot, ".plugin-*.apk")
	if err != nil {
		return errors.New("temporary storage unavailable")
	}
	path := file.Name()
	defer os.Remove(path)
	defer file.Close()
	client := &http.Client{Timeout: 45 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableCompression: true}}
	defer client.CloseIdleConnections()
	downloadURL, size := archiveURL, archiveSize
	metadata, err := exec.CommandContext(ctx, "apk", "query", "--fields", "arch", "--format", "json", "--installed", "sb-gateway-openwrt").Output()
	if err != nil {
		return errors.New("installed package architecture unavailable")
	}
	if strings.Contains(string(metadata), "aarch64_cortex-a53") {
		downloadURL = releaseURL + "packages_apk_aarch64_cortex-a53.zip"
		size = 23427404
		if name == "v2ray-plugin" {
			pin.Size = 6165908
			pin.SHA = "7a985c5ebdd4062990e0cf974816690510a9f15f2a5383c872322dfeb9c73ca1"
		} else {
			pin.Size = 31809
			pin.SHA = "7a5db0eb7e8436bf00ed7004267f8d55d69ce0cd328de25fba55e2166fb900e2"
		}
	} else if !strings.Contains(string(metadata), "aarch64_generic") {
		return errors.New("unsupported router architecture")
	}
	reader := &rangeReader{ctx: ctx, client: client, url: downloadURL, size: size, start: -1}
	if err = extractPackage(reader, size, pin, file); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return errors.New("package write failed")
	}
	phase("installing")
	// Only this exact SHA-pinned APK is untrusted. Configured official feeds
	// still verify dependencies; no extra repository or system upgrade is added.
	cmd := exec.CommandContext(ctx, "apk", "add", "--allow-untrusted", path)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return errors.New("package installation failed; check storage and package feeds")
	}
	if !Status().Installed[name] {
		return errors.New("installed plugin executable not found")
	}
	return nil
}
func extractPackage(reader io.ReaderAt, size int64, pin packagePin, destination io.Writer) error {
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return errors.New("plugin download failed")
	}
	for _, entry := range archive.File {
		if entry.Name != pin.File {
			continue
		}
		if entry.UncompressedSize64 != uint64(pin.Size) || entry.CompressedSize64 > uint64(pin.Size)+(1<<16) {
			return errors.New("unexpected plugin package")
		}
		body, err := entry.Open()
		if err != nil {
			return errors.New("plugin download failed")
		}
		defer body.Close()
		hash := sha256.New()
		written, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(body, pin.Size+1))
		if err != nil || written != pin.Size || hex.EncodeToString(hash.Sum(nil)) != pin.SHA {
			return errors.New("plugin package checksum mismatch")
		}
		return nil
	}
	return errors.New("plugin package absent from pinned release")
}

// Download only the selected APK plus the ZIP directory, not the 23 MB bundle.
// One 256 KiB cache prevents thousands of small ZIP ReaderAt HTTP requests.
type rangeReader struct {
	ctx    context.Context
	client *http.Client
	url    string
	size   int64
	start  int64
	cache  []byte
}

func (r *rangeReader) ReadAt(p []byte, offset int64) (int, error) {
	if offset < 0 || offset >= r.size {
		return 0, io.EOF
	}
	n := 0
	for len(p) > 0 && offset < r.size {
		if offset < r.start || offset >= r.start+int64(len(r.cache)) {
			start := offset / (256 << 10) * (256 << 10)
			end := min(start+(256<<10), r.size) - 1
			req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
			if err != nil {
				return n, err
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
			req.Header.Set("User-Agent", "SB-Gateway-Plugin/1")
			req.Header.Set("Accept-Encoding", "identity")
			response, err := r.client.Do(req)
			if err != nil {
				return n, err
			}
			expected := fmt.Sprintf("bytes %d-%d/%d", start, end, r.size)
			if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != expected {
				response.Body.Close()
				return n, errors.New("download server does not support bounded package download")
			}
			body, err := io.ReadAll(io.LimitReader(response.Body, end-start+2))
			response.Body.Close()
			if err != nil || int64(len(body)) != end-start+1 {
				return n, errors.New("incomplete plugin download")
			}
			r.start = start
			r.cache = body
		}
		count := copy(p, r.cache[offset-r.start:])
		n += count
		offset += int64(count)
		p = p[count:]
	}
	if len(p) > 0 {
		return n, io.EOF
	}
	return n, nil
}

// Keep errors independent of package output and URL query strings.
