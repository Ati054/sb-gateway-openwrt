// Package apkmeta reads this application's installed APK identity.
package apkmeta

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"time"
)

type Installed struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
}
type Command func(context.Context, string, ...string) *exec.Cmd

var versionPattern = regexp.MustCompile(`^[0-9][A-Za-z0-9._+~-]{0,79}$`)

func ReadInstalled(ctx context.Context, command Command) (Installed, error) {
	if command == nil {
		command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			c := exec.CommandContext(ctx, name, args...)
			c.WaitDelay = time.Second
			return c
		}
	}
	for _, name := range []string{"xroute-openwrt", "sb-gateway-openwrt"} {
		if err := ctx.Err(); err != nil {
			return Installed{}, err
		}
		body, err := command(ctx, "apk", "query", "--fields", "name,version,arch", "--format", "json", "--installed", name).Output()
		if err != nil {
			if ctx.Err() != nil {
				return Installed{}, ctx.Err()
			}
			continue
		}
		var entries []Installed
		if len(body) > 16384 || json.Unmarshal(body, &entries) != nil || len(entries) > 1 {
			break
		}
		if len(entries) == 0 {
			continue
		}
		entry := entries[0]
		if entry.Name != name || !versionPattern.MatchString(entry.Version) || entry.Arch == "" {
			break
		}
		return entry, nil
	}
	return Installed{}, errors.New("Installed package metadata unavailable")
}
