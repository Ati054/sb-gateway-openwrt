package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Ati054/xroute-openwrt/internal/capture"
	"github.com/Ati054/xroute-openwrt/internal/settings"
	"github.com/Ati054/xroute-openwrt/internal/xray"
)

// CaptureRestoreInput carries only routing data to the renderer running under
// the firewall lock. Subscription URLs, node credentials and assets stay out.
type CaptureRestoreInput struct {
	OwnerPID     int                       `json:"owner_pid,omitempty"`
	Enabled      bool                      `json:"enabled"`
	RouteEnabled bool                      `json:"route_enabled"`
	Interface    string                    `json:"interface"`
	Port         int                       `json:"port"`
	DNSPort      int                       `json:"dns_port"`
	DNSMode      string                    `json:"dns_mode"`
	Policy       capture.Policy            `json:"policy"`
	Scopes       []capture.EmergencyPolicy `json:"scopes"`
}

func EncodeCaptureRestore(config settings.Config, ownerPID ...int) ([]byte, error) {
	scopes, err := EmergencyPolicies(config)
	if err != nil {
		return nil, err
	}
	policy := config.Capture
	policy.Automatic = capture.Inventory{}
	owner := 0
	if len(ownerPID) > 0 {
		owner = ownerPID[0]
	}
	return json.Marshal(CaptureRestoreInput{OwnerPID: owner, Enabled: config.Enabled, RouteEnabled: config.RouteEnabled,
		Interface: config.LANInterface, Port: config.Base.TransparentPort, DNSPort: config.DNSPort,
		DNSMode: config.DNSMode, Policy: policy, Scopes: scopes})
}

func DecodeCaptureRestore(reader io.Reader) (CaptureRestoreInput, error) {
	const limit = 1 << 20
	var result CaptureRestoreInput
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return result, err
	}
	if len(body) > limit {
		return result, errors.New("capture configuration exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return result, errors.New("unexpected trailing capture configuration")
	}
	// Interface discovery must always reflect the current router, not supplied
	// JSON. The caller fills this after taking the firewall transaction lock.
	result.Policy.Automatic = capture.Inventory{}
	if result.OwnerPID < 0 {
		return result, errors.New("invalid capture owner")
	}
	return result, nil
}

func CaptureRestoreOwnerCurrent(input CaptureRestoreInput, paths Paths) bool {
	if input.OwnerPID == 0 {
		return true
	}
	body, err := os.ReadFile(filepath.Join(paths.RunRoot, "daemon.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	return err == nil && pid == input.OwnerPID
}

func (input CaptureRestoreInput) Config() settings.Config {
	return settings.Config{Enabled: input.Enabled, RouteEnabled: input.RouteEnabled,
		LANInterface: input.Interface, DNSMode: input.DNSMode, DNSPort: input.DNSPort,
		Base: xray.BaseConfig{TransparentPort: input.Port}, Capture: input.Policy}
}
