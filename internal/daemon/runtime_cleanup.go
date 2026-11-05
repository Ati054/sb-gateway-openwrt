package daemon

import "errors"

// Call after workers and the managed DNS listener have stopped. Restoring the
// previous resolver and closing the core are independent; both must finish
// before init can replace the binary or start another daemon.
func closeCoreAndRestoreDNS(closeCore, restoreDNS func() error) (err error) {
	if restoreDNS == nil {
		return closeCore()
	}
	restored := make(chan error, 1)
	go func() { restored <- restoreDNS() }()
	defer func() { err = errors.Join(err, <-restored) }()
	return closeCore()
}
