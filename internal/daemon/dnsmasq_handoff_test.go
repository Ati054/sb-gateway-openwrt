package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeDNSMasqOps(values map[string]string, restartError error) (dnsmasqHandoffOps, *int) {
	restarts := new(int)
	return dnsmasqHandoffOps{
		get: func(key string) (string, error) {
			value, exists := values[key]
			if !exists {
				return "", os.ErrNotExist
			}
			return value, nil
		},
		action: func(args ...string) error {
			if len(args) == 0 {
				return errors.New("missing action")
			}
			switch args[0] {
			case "set", "add_list", "del_list":
				_, pair, ok := strings.Cut(args[1], "=")
				if !ok {
					return errors.New("missing value")
				}
				key := "noresolv"
				if strings.Contains(args[1], ".server=") {
					key = "server"
				}
				switch args[0] {
				case "set":
					values[key] = pair
				case "add_list":
					values[key] = strings.TrimSpace(values[key] + " " + pair)
				case "del_list":
					var remaining []string
					for _, value := range strings.Fields(values[key]) {
						if value != pair {
							remaining = append(remaining, value)
						}
					}
					values[key] = strings.Join(remaining, " ")
				}
			case "-q":
				delete(values, "noresolv")
			case "commit":
			default:
				return errors.New("unexpected action")
			}
			return nil
		},
		restart: func() error {
			*restarts++
			return restartError
		},
	}, restarts
}

func TestDNSMasqHandoffRestoresOnlyOwnedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dnsmasq-handoff.json")
	values := map[string]string{"server": "/lan/192.168.1.1"}
	ops, restarts := fakeDNSMasqOps(values, nil)
	if err := acquireDNSMasqHandoff(path, 1053, ops); err != nil {
		t.Fatal(err)
	}
	if values["noresolv"] != "1" || !strings.Contains(values["server"], "127.0.0.1#1053") {
		t.Fatalf("handoff did not set router upstream: %v", values)
	}
	if err := restoreDNSMasqHandoff(path, ops); err != nil {
		t.Fatal(err)
	}
	if _, exists := values["noresolv"]; exists || values["server"] != "/lan/192.168.1.1" || *restarts != 2 {
		t.Fatalf("router DNS was not restored: %v restarts=%d", values, *restarts)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("handoff marker retained: %v", err)
	}
}

func TestDNSMasqExistingDefaultUpstreamIsRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	values := map[string]string{"server": "127.0.0.1#5053 /lan/192.168.1.1", "noresolv": "0"}
	ops, _ := fakeDNSMasqOps(values, nil)
	if err := acquireDNSMasqHandoff(path, 1053, ops); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(values["server"], "5053") || !strings.Contains(values["server"], "/lan/") {
		t.Fatal("old default remains or conditional server lost")
	}
	if err := restoreDNSMasqHandoff(path, ops); err != nil {
		t.Fatal(err)
	}
	if values["noresolv"] != "0" || !strings.Contains(values["server"], "5053") || strings.Contains(values["server"], "1053") {
		t.Fatal("previous upstream not restored")
	}
}
func TestDNSMasqMultiInstanceDoesNotChangeGuestResolver(t *testing.T) {
	root := map[string]map[string]string{
		"first":  {"interface": "lan", "server": "1.1.1.1"},
		"second": {"interface": "lan", "server": "/internal/192.0.2.1 8.8.8.8", "noresolv": "0"},
		"guest":  {"interface": "guest", "server": "9.9.9.9"},
	}
	restarts := 0
	top, _ := fakeDNSMasqOps(root["first"], nil)
	top.sections = func() ([]string, error) { return []string{"first", "second", "guest"}, nil }
	top.scoped = func(name string) dnsmasqHandoffOps { ops, _ := fakeDNSMasqOps(root[name], nil); return ops }
	top.restart = func() error { restarts++; return nil }
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := acquireDNSMasqHandoff(path, 1053, top); err != nil {
		t.Fatal(err)
	}
	if root["guest"]["server"] != "9.9.9.9" || !strings.Contains(root["first"]["server"], "1053") || !strings.Contains(root["second"]["server"], "1053") {
		t.Fatal("handoff selected wrong instances")
	}
	if err := restoreDNSMasqHandoff(path, top); err != nil {
		t.Fatal(err)
	}
	if root["first"]["server"] != "1.1.1.1" || root["second"]["noresolv"] != "0" || root["guest"]["server"] != "9.9.9.9" || restarts != 2 {
		t.Fatal("multi-instance restoration wrong")
	}
}
func TestDNSMasqRetryRestartsEvenAfterConfigurationWasRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	values := map[string]string{}
	ops, _ := fakeDNSMasqOps(values, nil)
	if err := acquireDNSMasqHandoff(path, 1053, ops); err != nil {
		t.Fatal(err)
	}
	retries := 0
	ops.restart = func() error {
		retries++
		if retries == 1 {
			return errors.New("temporary restart failure")
		}
		return nil
	}
	if err := restoreDNSMasqHandoff(path, ops); err == nil {
		t.Fatal("restart failure was ignored")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("retry marker lost")
	}
	if err := restoreDNSMasqHandoff(path, ops); err != nil || retries != 2 {
		t.Fatal("runtime restoration was not retried", err)
	}
}

func TestDNSMasqReadFailureDoesNotMutateOrCreateMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	values := map[string]string{"server": "1.1.1.1"}
	ops, _ := fakeDNSMasqOps(values, nil)
	previous := ops.get
	ops.get = func(key string) (string, error) {
		if key == "server" {
			return "", context.DeadlineExceeded
		}
		return previous(key)
	}
	if err := acquireDNSMasqHandoff(path, 1053, ops); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, exists := values["noresolv"]; exists {
		t.Fatal("modified DNS after read failure")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("marker created after failed read")
	}
}
func TestDNSMasqRestoreRetriesCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	values := map[string]string{"server": "1.1.1.1"}
	ops, _ := fakeDNSMasqOps(values, nil)
	if err := acquireDNSMasqHandoff(path, 1053, ops); err != nil {
		t.Fatal(err)
	}
	original := ops.action
	commits := 0
	ops.action = func(args ...string) error {
		if args[0] == "commit" {
			commits++
			if commits == 1 {
				return errors.New("commit failure")
			}
		}
		return original(args...)
	}
	if err := restoreDNSMasqHandoff(path, ops); err == nil {
		t.Fatal("commit failure ignored")
	}
	if err := restoreDNSMasqHandoff(path, ops); err != nil || commits != 2 {
		t.Fatal("commit not retried", err)
	}
}
func TestDNSMasqLegacyMarkerFollowsReorderedOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	body, _ := json.Marshal(dnsmasqHandoffState{Server: "127.0.0.1#1053"})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	values := map[string]map[string]string{"newfirst": {"server": "9.9.9.9", "noresolv": "1"}, "owner": {"server": "127.0.0.1#1053", "noresolv": "1"}}
	ops, _ := fakeDNSMasqOps(values["newfirst"], nil)
	ops.sections = func() ([]string, error) { return []string{"newfirst", "owner"}, nil }
	ops.scoped = func(name string) dnsmasqHandoffOps { o, _ := fakeDNSMasqOps(values[name], nil); return o }
	if err := restoreDNSMasqHandoff(path, ops); err != nil {
		t.Fatal(err)
	}
	if values["newfirst"]["server"] != "9.9.9.9" || values["newfirst"]["noresolv"] != "1" {
		t.Fatal("changed unrelated instance")
	}
	if values["owner"]["server"] != "" || values["owner"]["noresolv"] != "" {
		t.Fatal("owner not restored")
	}
}

func TestDNSMasqInvalidMarkerDoesNotChangeConfiguration(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 65537), `{"sections":[{"section":"owner","server":"127.0.0.1#1053"},{"section":"bad;command","server":"127.0.0.1#1053"}]}`} {
		path := filepath.Join(t.TempDir(), "handoff.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		values := map[string]string{"server": "127.0.0.1#1053", "noresolv": "1"}
		ops, _ := fakeDNSMasqOps(values, nil)
		if err := restoreDNSMasqHandoff(path, ops); err == nil {
			t.Fatal("invalid marker accepted")
		}
		if values["server"] != "127.0.0.1#1053" || values["noresolv"] != "1" {
			t.Fatal("invalid marker changed DNS")
		}
	}
}

func TestDNSMasqLostMarkerDoesNotPreserveOwnForwarder(t *testing.T) {
	for _, server := range []string{"127.0.0.1#1053", "127.0.0.1#1053 8.8.8.8 /office/192.0.2.53"} {
		values := map[string]string{"server": server, "noresolv": "1"}
		ops, _ := fakeDNSMasqOps(values, nil)
		path := filepath.Join(t.TempDir(), "handoff.json")
		if err := acquireDNSMasqHandoff(path, 1053, ops); err != nil {
			t.Fatal(err)
		}
		if err := restoreDNSMasqHandoff(path, ops); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(values["server"], "127.0.0.1#1053") || values["noresolv"] == "1" {
			t.Fatalf("dead forwarder preserved: %v", values)
		}
		if strings.Contains(server, "/office/") && !strings.Contains(values["server"], "/office/192.0.2.53") {
			t.Fatal("conditional server lost")
		}
	}
}

func TestDNSMasqOrphanRepairRetriesFailedRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	values := map[string]string{"server": "127.0.0.1#1153 /office/192.0.2.53", "noresolv": "1"}
	ops, _ := fakeDNSMasqOps(values, nil)
	ops.OwnPort = 1153
	attempts := 0
	ops.restart = func() error {
		attempts++
		if attempts == 1 {
			return errors.New("restart unavailable")
		}
		return nil
	}
	if err := restoreDNSMasqHandoff(path, ops); err == nil {
		t.Fatal("failure hidden")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("repair marker lost", err)
	}
	if err := restoreDNSMasqHandoff(path, ops); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || values["server"] != "/office/192.0.2.53" || values["noresolv"] != "" {
		t.Fatal("orphan runtime not repaired")
	}
}
func TestDNSMasqRecoveryIsArmedBeforeMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	values := map[string]string{"server": "127.0.0.1#1053", "noresolv": "1"}
	ops, _ := fakeDNSMasqOps(values, nil)
	ops.OwnPort = 1053
	ops.ensureRecovery = func() error { return errors.New("cannot persist boot recovery") }
	if err := restoreDNSMasqHandoff(path, ops); err == nil {
		t.Fatal("missing recovery ignored")
	}
	if values["server"] != "127.0.0.1#1053" {
		t.Fatal("changed DNS before recovery was saved")
	}
}

func TestDNSMasqMissingMarkerDoesNotInspectUnownedDNS(t *testing.T) {
	ops := dnsmasqHandoffOps{sections: func() ([]string, error) { t.Fatal("inspected unowned DNS"); return nil, nil }}
	if err := restoreDNSMasqHandoff(filepath.Join(t.TempDir(), "absent.json"), ops); err != nil {
		t.Fatal(err)
	}
}
