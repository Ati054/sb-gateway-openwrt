//go:build !linux

package capture

import (
	"context"
	"errors"
	"time"
)

const LegacyDNSCleanupTimeout = 5 * time.Second

func PruneLegacyDNS(context.Context, string, int, []string) (int, error) {
	return 0, errors.New("legacy DNS cleanup requires Linux")
}
