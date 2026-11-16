//go:build !linux

package dnsproxy

import "net"

func enableDNSPacketInfo(conn net.PacketConn) error             { return nil }
func dnsReplyControl(metadata []byte, peer *net.UDPAddr) []byte { return nil }
