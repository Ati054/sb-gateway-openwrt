//go:build linux

package dnsproxy

import "golang.org/x/sys/unix"

func dnsDatagramTruncated(flags int) bool { return flags&unix.MSG_TRUNC != 0 }
