//go:build linux

package dnsproxy

import (
	"errors"
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func enableDNSPacketInfo(conn net.PacketConn) error {
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		return nil
	}
	err4 := ipv4.NewPacketConn(udp).SetControlMessage(ipv4.FlagDst, true)
	if udp.LocalAddr().(*net.UDPAddr).IP.To4() != nil {
		return err4
	}
	err6 := ipv6.NewPacketConn(udp).SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface, true)
	return errors.Join(err4, err6)
}

func dnsReplyControl(metadata []byte, peer *net.UDPAddr) []byte {
	if peer == nil {
		return nil
	}
	if peer.IP.To4() != nil {
		var cm ipv4.ControlMessage
		if cm.Parse(metadata) == nil && cm.Dst.To4() != nil && !cm.Dst.IsUnspecified() && !cm.Dst.IsMulticast() {
			return (&ipv4.ControlMessage{Src: cm.Dst}).Marshal()
		}
	} else {
		var cm ipv6.ControlMessage
		if cm.Parse(metadata) == nil && cm.Dst.To16() != nil && !cm.Dst.IsUnspecified() && !cm.Dst.IsMulticast() {
			return (&ipv6.ControlMessage{Src: cm.Dst, IfIndex: cm.IfIndex}).Marshal()
		}
	}
	return nil
}
