package daemon

import (
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
				return "", errors.New("not set")
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

func TestDNSMasqHandoffRejectsOtherDefaultUpstream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dnsmasq-handoff.json")
	values := map[string]string{"server": "127.0.0.1#5053"}
	ops, _ := fakeDNSMasqOps(values, nil)
	if err := acquireDNSMasqHandoff(path, 1053, ops); err == nil {
		t.Fatal("existing resolver was replaced")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("handoff marker should not exist: %v", err)
	}
}
