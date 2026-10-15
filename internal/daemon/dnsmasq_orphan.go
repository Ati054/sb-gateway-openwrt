package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Repair an interrupted legacy handoff even when the other resolver/package
// is no longer installed. Read all sections before making any changes.
func repairOrphanedDNSMasq(path string, ops dnsmasqHandoffOps) error {
	if ops.OwnPort < 1024 || ops.OwnPort > 65535 {
		return nil
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
	own := fmt.Sprintf("127.0.0.1#%d", ops.OwnPort)
	type repair struct {
		name          string
		clearNoresolv bool
	}
	var repairs []repair
	for _, name := range names {
		scoped := scopeDNSMasq(ops, name)
		servers, err := dnsmasqOptional(scoped, "server")
		if err != nil {
			return err
		}
		found := false
		for _, server := range strings.Fields(servers) {
			if server == own {
				found = true
			}
		}
		if !found {
			continue
		}
		noresolv, err := dnsmasqOptional(scoped, "noresolv")
		if err != nil {
			return err
		}
		repairs = append(repairs, repair{name, noresolv == "1"})
	}
	if len(repairs) == 0 {
		return nil
	}
	if ops.ensureRecovery != nil {
		if err := ops.ensureRecovery(); err != nil {
			return err
		}
	}
	state := dnsmasqHandoffState{}
	for _, item := range repairs {
		scoped := scopeDNSMasq(ops, item.name)
		noresolv, err := dnsmasqOptional(scoped, "noresolv")
		if err != nil {
			return err
		}
		state.Sections = append(state.Sections, dnsmasqOwnedState{Section: item.name, Server: own, HadNoResolv: noresolv != "" && !item.clearNoresolv, NoResolv: noresolv})
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := writeDurableDNSMarker(path, body); err != nil {
		return err
	}
	// The normal restoration transaction retains its marker across failed commit
	// or restart, so a retry also repairs the still-running dnsmasq process.
	return restoreDNSMasqHandoff(path, ops)
}
