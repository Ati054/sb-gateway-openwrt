//go:build !linux

package sip003

import (
	"errors"
	"os"
)

func lockPluginInstallFile(*os.File) error {
	return errors.New("plugin installation is supported only on Linux")
}
