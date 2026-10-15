package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type dnsmasqHandoffState struct {
	// Flat fields retain compatibility with markers from earlier releases.
	HadNoResolv bool                `json:"had_noresolv"`
	NoResolv    string              `json:"noresolv,omitempty"`
	Server      string              `json:"server"`
	Sections    []dnsmasqOwnedState `json:"sections,omitempty"`
}
type dnsmasqOwnedState struct {
	Section         string   `json:"section"`
	HadNoResolv     bool     `json:"had_noresolv"`
	NoResolv        string   `json:"noresolv,omitempty"`
	Server          string   `json:"server"`
	RemovedDefaults []string `json:"removed_defaults,omitempty"`
}
type dnsmasqHandoffOps struct {
	LANInterface   string
	OwnPort        int
	ensureRecovery func() error
	get            func(string) (string, error)
	action         func(...string) error
	restart        func() error
	sections       func() ([]string, error)
	scoped         func(string) dnsmasqHandoffOps
}

var dnsmasqSectionName = regexp.MustCompile(`^(?:[A-Za-z0-9_]{1,64}|@dnsmasq\[[0-9]{1,3}\])$`)

func dnsmasqHandoffFile(paths Paths) string {
	return filepath.Join(paths.DataRoot, "dnsmasq-handoff.json")
}
func systemDNSMasqOps() dnsmasqHandoffOps { return systemDNSMasqOpsWithContext(context.Background()) }
func systemDNSMasqOpsWithContext(parent context.Context) dnsmasqHandoffOps {
	run := func(binary string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(parent, 20*time.Second)
		defer cancel()
		output, err := boundedCommand(ctx, binary, args...).CombinedOutput()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%s: %w", binary, err)
		}
		return output, nil
	}
	var scope func(string) dnsmasqHandoffOps
	scope = func(section string) dnsmasqHandoffOps {
		return dnsmasqHandoffOps{
			get: func(key string) (string, error) {
				v, err := run("uci", "-q", "get", "dhcp."+section+"."+key)
				if err != nil {
					var exit *exec.ExitError
					if errors.As(err, &exit) && exit.ExitCode() == 1 {
						return "", os.ErrNotExist
					}
				}
				return strings.TrimSpace(string(v)), err
			},
			action:  func(args ...string) error { _, err := run("uci", args...); return err },
			restart: func() error { _, err := run("/etc/init.d/dnsmasq", "restart"); return err },
		}
	}
	ops := scope("@dnsmasq[0]")
	// A fresh install must not treat another resolver using port 1053 as ours.
	// Marker-less repair is allowed only after the user authorized DNS handoff.
	if confirmed, err := run("uci", "-q", "get", "resproxy.main.dns_handoff_confirmed"); err == nil && strings.TrimSpace(string(confirmed)) == "1" {
		ops.OwnPort = 1053
		if data, err := run("uci", "-q", "get", "resproxy.main.dns_port"); err == nil {
			if port, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && port >= 1024 && port <= 65535 {
				ops.OwnPort = port
			}
		}
	}

	ops.ensureRecovery = ensureDNSBootRecovery
	ops.scoped = scope
	ops.sections = func() ([]string, error) {
		data, err := run("uci", "-X", "show", "dhcp")
		if err != nil {
			return nil, err
		}
		names := []string{}
		for _, line := range strings.Split(string(data), "\n") {
			key, kind, ok := strings.Cut(line, "=")
			if ok && kind == "dnsmasq" && strings.HasPrefix(key, "dhcp.") {
				name := strings.TrimPrefix(key, "dhcp.")
				if !dnsmasqSectionName.MatchString(name) {
					return nil, errors.New("invalid dnsmasq section")
				}
				names = append(names, name)
			}
		}
		if len(names) > 32 {
			return nil, errors.New("too many dnsmasq instances")
		}
		return names, nil
	}
	return ops
}
func scopeDNSMasq(ops dnsmasqHandoffOps, section string) dnsmasqHandoffOps {
	if ops.scoped != nil {
		return ops.scoped(section)
	}
	return ops
}
func dnsmasqOptional(ops dnsmasqHandoffOps, key string) (string, error) {
	value, err := ops.get(key)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return value, err
}
func validDNSMasqSections(names []string) error {
	if len(names) > 32 {
		return errors.New("too many dnsmasq instances")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if !dnsmasqSectionName.MatchString(name) || seen[name] {
			return errors.New("invalid dnsmasq section list")
		}
		seen[name] = true
	}
	return nil
}
func servesLAN(ops dnsmasqHandoffOps, device string) (bool, error) {
	if device == "" {
		device = "br-lan"
	}
	port, err := dnsmasqOptional(ops, "port")
	if err != nil {
		return false, err
	}
	if port == "0" {
		return false, nil
	}
	excluded, err := dnsmasqOptional(ops, "notinterface")
	if err != nil {
		return false, err
	}
	for _, name := range strings.Fields(excluded) {
		if name == "lan" || name == device || name == "*" {
			return false, nil
		}
	}
	allowed, err := dnsmasqOptional(ops, "interface")
	if err != nil {
		return false, err
	}
	if allowed == "" {
		return true, nil
	}
	for _, name := range strings.Fields(allowed) {
		if name == "lan" || name == device || name == "*" {
			return true, nil
		}
	}
	return false, nil
}
func prepareDNSMasq(section string, port int, ops dnsmasqHandoffOps) (dnsmasqOwnedState, error) {
	noresolv, noErr := ops.get("noresolv")
	if noErr != nil && !errors.Is(noErr, os.ErrNotExist) {
		return dnsmasqOwnedState{}, noErr
	}
	servers, err := dnsmasqOptional(ops, "server")
	if err != nil {
		return dnsmasqOwnedState{}, err
	}
	state := dnsmasqOwnedState{Section: section, HadNoResolv: noErr == nil, NoResolv: noresolv, Server: fmt.Sprintf("127.0.0.1#%d", port)}
	for _, server := range strings.Fields(servers) {
		if server == state.Server {
			// A previous release changed DHCP but its ownership marker was lost.
			// Never preserve our own dead forwarder as a user's upstream.
			if noresolv == "1" {
				state.HadNoResolv = false
				state.NoResolv = ""
			}
			continue
		}
		if !strings.HasPrefix(server, "/") {
			state.RemovedDefaults = append(state.RemovedDefaults, server)
		}
	}
	return state, nil
}

// The owner has already authorized DNS handoff. Change all LAN-serving
// instances, preserving conditional servers and restoring previous defaults.
func acquireDNSMasqHandoff(path string, port int, ops dnsmasqHandoffOps) error {
	if port < 1024 || port > 65535 {
		return errors.New("invalid DNS port")
	}
	if _, err := os.Stat(path); err == nil {
		return errors.New("previous dnsmasq handoff must be restored first")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if ops.ensureRecovery != nil {
		if err := ops.ensureRecovery(); err != nil {
			return err
		}
	}
	ops.OwnPort = port
	if err := repairOrphanedDNSMasq(path, ops); err != nil {
		return err
	}
	names := []string{"@dnsmasq[0]"}
	if ops.sections != nil {
		var err error
		names, err = ops.sections()
		if err != nil {
			return err
		}
	}
	if err := validDNSMasqSections(names); err != nil {
		return err
	}
	state := dnsmasqHandoffState{}
	for _, name := range names {
		scoped := scopeDNSMasq(ops, name)
		if ops.sections != nil {
			selected, err := servesLAN(scoped, ops.LANInterface)
			if err != nil {
				return err
			}
			if !selected {
				continue
			}
		}
		item, err := prepareDNSMasq(name, port, scoped)
		if err != nil {
			return err
		}
		state.Sections = append(state.Sections, item)
	}
	if len(state.Sections) == 0 {
		return errors.New("no LAN dnsmasq instance available")
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(body) > 65536 {
		return errors.New("DNS handoff marker exceeds the limit")
	}
	if err := writeDurableDNSMarker(path, body); err != nil {
		return err
	}
	rollback := func(cause error) error {
		if err := restoreDNSMasqHandoff(path, ops); err != nil {
			return fmt.Errorf("%w; DNS rollback failed: %w", cause, err)
		}
		return cause
	}
	for _, item := range state.Sections {
		scoped := scopeDNSMasq(ops, item.Section)
		prefix := "dhcp." + item.Section + "."
		if err := scoped.action("set", prefix+"noresolv=1"); err != nil {
			return rollback(err)
		}
		for _, old := range item.RemovedDefaults {
			if err := scoped.action("del_list", prefix+"server="+old); err != nil {
				return rollback(err)
			}
		}
		if err := scoped.action("add_list", prefix+"server="+item.Server); err != nil {
			return rollback(err)
		}
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
	content, err := readDNSMarker(path)
	if errors.Is(err, os.ErrNotExist) {
		return repairOrphanedDNSMasq(path, ops)
	}
	if err != nil {
		return err
	}
	if len(content) > 65536 {
		return errors.New("DNS handoff marker exceeds the limit")
	}
	legacyMarker := false
	var state dnsmasqHandoffState
	if err := json.Unmarshal(content, &state); err != nil {
		return err
	}
	if len(state.Sections) == 0 {
		if state.Server == "" {
			return errors.New("invalid dnsmasq handoff marker")
		}
		legacyMarker = true
		state.Sections = []dnsmasqOwnedState{{Section: "@dnsmasq[0]", HadNoResolv: state.HadNoResolv, NoResolv: state.NoResolv, Server: state.Server}}
	}
	if len(state.Sections) > 32 {
		return errors.New("invalid DNS handoff section count")
	}
	for index := range state.Sections {
		item := &state.Sections[index]
		for _, old := range item.RemovedDefaults {
			if old == item.Server && item.NoResolv == "1" {
				item.HadNoResolv = false
				item.NoResolv = ""
			}
		}
	}
	for _, item := range state.Sections {
		port, err := strconv.Atoi(strings.TrimPrefix(item.Server, "127.0.0.1#"))
		if !dnsmasqSectionName.MatchString(item.Section) || !strings.HasPrefix(item.Server, "127.0.0.1#") || err != nil || port < 1024 || port > 65535 {
			return errors.New("invalid DNS handoff reference")
		}
	}
	existing := map[string]bool{}
	if ops.sections != nil {
		names, err := ops.sections()
		if err != nil {
			return err
		}
		if err := validDNSMasqSections(names); err != nil {
			return err
		}
		for _, name := range names {
			existing[name] = true
		}
	}

	// Resolve an old anonymous marker by its owned upstream before restoring.
	// Persist that stable identity so retries cannot target a reordered instance.
	if legacyMarker && ops.sections != nil {
		item := state.Sections[0]
		match := ""
		for name := range existing {
			servers, err := dnsmasqOptional(scopeDNSMasq(ops, name), "server")
			if err != nil {
				return err
			}
			for _, server := range strings.Fields(servers) {
				if server == item.Server {
					if match != "" {
						return errors.New("ambiguous legacy DNS handoff")
					}
					match = name
				}
			}
		}
		if match == "" {
			return errors.New("legacy DNS handoff owner is unavailable")
		}
		state.Sections[0].Section = match
		body, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if err := writeDurableDNSMarker(path, body); err != nil {
			return err
		}
	}
	changed := false
	restoreRuntime := false
	for _, item := range state.Sections {
		if !dnsmasqSectionName.MatchString(item.Section) || !strings.HasPrefix(item.Server, "127.0.0.1#") {
			return errors.New("invalid DNS handoff reference")
		}
		// Old markers used an anonymous index; retain their restoration behavior.
		if ops.sections != nil && !strings.HasPrefix(item.Section, "@dnsmasq[") && !existing[item.Section] {
			continue
		}
		restoreRuntime = true
		scoped := scopeDNSMasq(ops, item.Section)
		prefix := "dhcp." + item.Section + "."
		servers, err := dnsmasqOptional(scoped, "server")
		if err != nil {
			return err
		}
		present := map[string]bool{}
		for _, v := range strings.Fields(servers) {
			present[v] = true
		}
		if present[item.Server] {
			if err := scoped.action("del_list", prefix+"server="+item.Server); err != nil {
				return err
			}
			changed = true
		}
		for _, old := range item.RemovedDefaults {
			if old != item.Server && !present[old] {
				if err := scoped.action("add_list", prefix+"server="+old); err != nil {
					return err
				}
				changed = true
			}
		}
		current, currentErr := scoped.get("noresolv")
		if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
			return currentErr
		}
		// Respect a user's concurrent edit; only undo the value installed by us.
		if currentErr == nil && current == "1" {
			if item.HadNoResolv {
				if item.NoResolv != "1" {
					if err := scoped.action("set", prefix+"noresolv="+item.NoResolv); err != nil {
						return err
					}
					changed = true
				}
			} else {
				if err := scoped.action("-q", "delete", prefix+"noresolv"); err != nil {
					return err
				}
				changed = true
			}
		}
	}
	if changed || restoreRuntime {
		if err := ops.action("commit", "dhcp"); err != nil {
			return err
		}
	}
	if restoreRuntime {
		if err := ops.restart(); err != nil {
			return err
		}
	}
	return os.Remove(path)
}
func RestoreDNSMasqHandoff(paths Paths) error {
	return restoreDNSMasqHandoff(dnsmasqHandoffFile(paths), systemDNSMasqOps())
}
