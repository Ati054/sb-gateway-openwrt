//go:build !linux

package sip003

import "os/exec"

func configurePluginCommandCancellation(*exec.Cmd) {}
