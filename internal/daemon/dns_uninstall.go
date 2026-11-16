package daemon

import (
	"bytes"
	"errors"
	"os"
)

// The APK pre-deinstall hook runs this while the binary and bundled recovery
// template still exist. Do not remove the independent recovery path on error.
func PrepareDNSUninstall(paths Paths) error {
	lock, err := acquireController(paths)
	if err != nil {
		return err
	}
	defer lock.Close()
	return finishDNSUninstall(paths, "/usr/share/resproxy/dns-boot-recover", "/etc/uci-defaults/90-sb-gateway-dns-recover", func() error { return RestoreManagedDNS(paths) })
}

func finishDNSUninstall(paths Paths, source, destination string, restore func() error) error {
	if err := restore(); err != nil {
		return err
	}
	for _, marker := range []string{dnsmasqHandoffFile(paths), dnsHandoffFile(paths)} {
		if _, err := os.Lstat(marker); err == nil {
			return errors.New("DNS recovery is still pending")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	template, err := readDNSMarker(source)
	if err != nil {
		return err
	}
	current, err := readDNSMarker(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(template, current) {
		return nil
	} // Never delete a custom script.
	latest, err := os.Lstat(destination)
	if err != nil {
		return err
	}
	if !os.SameFile(info, latest) {
		return errors.New("DNS recovery helper changed")
	}
	return os.Remove(destination)
}
