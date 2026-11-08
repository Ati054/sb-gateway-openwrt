//go:build !linux

package dnsproxy

func dnsDatagramTruncated(flags int) bool { return flags != 0 }
