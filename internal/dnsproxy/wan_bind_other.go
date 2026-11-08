//go:build !linux

package dnsproxy

import (
	"errors"
	"syscall"
)

func bindWANInterface(string) func(string, string, syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error {
		return errors.New("binding ISP DNS to its uplink requires Linux")
	}
}
