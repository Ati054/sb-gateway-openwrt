package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type dnsmasqHandoffState struct {
	HadNoResolv bool   `json:"had_noresolv"`
	NoResolv    string `json:"noresolv,omitempty"`
	Server      string `json:"server"`
}

type dnsmasqHandoffOps struct {
	get     func(string) (string, error)
	action  func(...string) error
	restart func() error
}

func dnsmasqHandoffFile(paths Paths) string {
	return filepath.Join(paths.DataRoot, "dnsmasq-handoff.json")
}

func systemDNSMasqOps() dnsmasqHandoffOps {
	run := func(binary string, arguments ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, binary, arguments...).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w: %s", binary, strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
		}
		return output, nil
	}
	return dnsmasqHandoffOps{
		get: func(key string) (string, error) {
			value, err := run("uci", "-q", "get", "dhcp.@dnsmasq[0]."+key)
			return strings.TrimSpace(string(value)), err
		},
		action: func(args ...string) error {
			_, err := run("uci", args...)
			return err
		},
		restart: func() error {
			_, err := run("/etc/init.d/dnsmasq", "restart")
			return err
		},
	}
}

// acquireDNSMasqHandoff changes only the default upstream and noresolv.
// Domain-specific servers remain untouched; foreign default servers are
// rejected so our direct-WAN path is the sole public upstream.
func acquireDNSMasqHandoff(path string, port int, ops dnsmasqHandoffOps) error {
	if port < 1024 || port > 65535 {
		return errors.New("invalid DNS port")
	}
	if _, err := os.Stat(path); err == nil {
		return errors.New("previous dnsmasq handoff must be restored first")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	noresolv, noresolvErr := ops.get("noresolv")
	servers, serverErr := ops.get("server")
	if serverErr != nil {
		servers = ""
	}
	upstream := fmt.Sprintf("127.0.0.1#%d", port)
	for _, server := range strings.Fields(servers) {
		if !strings.HasPrefix(server, "/") {
			return fmt.Errorf("existing dnsmasq default upstream %q needs manual review", server)
		}
	}
	state := dnsmasqHandoffState{HadNoResolv: noresolvErr == nil, NoResolv: noresolv, Server: upstream}
	content, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return err
	}
	rollback := func(cause error) error {
		if restoreErr := restoreDNSMasqHandoff(path, ops); restoreErr != nil {
			return fmt.Errorf("%w; dnsmasq rollback: %v", cause, restoreErr)
		}
		return cause
	}
	if err := ops.action("set", "dhcp.@dnsmasq[0].noresolv=1"); err != nil {
		return rollback(err)
	}
	if err := ops.action("add_list", "dhcp.@dnsmasq[0].server="+upstream); err != nil {
		return rollback(err)
	}
	if err := ops.action("commit", "dhcp"); err != nil {
		return rollback(err)
	}
	if err := ops.restart(); err != nil {
		return rollback(err)
	}
	return nil
}

func restoreDNSMasqHandoff(path string, ops dnsmasqHandoffOps) error {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state dnsmasqHandoffState
	if err := json.Unmarshal(content, &state); err != nil {
		return err
	}
	if state.Server == "" {
		return errors.New("invalid dnsmasq handoff marker")
	}
	// del_list fails if a partially completed takeover never added the entry.
	servers, _ := ops.get("server")
	serverPresent := false
	for _, server := range strings.Fields(servers) {
		if server == state.Server {
			serverPresent = true
			break
		}
	}
	if serverPresent {
		if err := ops.action("del_list", "dhcp.@dnsmasq[0].server="+state.Server); err != nil {
			return err
		}
	}
	if state.HadNoResolv {
		if err := ops.action("set", "dhcp.@dnsmasq[0].noresolv="+state.NoResolv); err != nil {
			return err
		}
	} else {
		if current, err := ops.get("noresolv"); err == nil && current == "1" {
			if err := ops.action("-q", "delete", "dhcp.@dnsmasq[0].noresolv"); err != nil {
				return err
			}
		}
	}
	if err := ops.action("commit", "dhcp"); err != nil {
		return err
	}
	if err := ops.restart(); err != nil {
		return err
	}
	return os.Remove(path)
}

func RestoreDNSMasqHandoff(paths Paths) error {
	return restoreDNSMasqHandoff(dnsmasqHandoffFile(paths), systemDNSMasqOps())
}
