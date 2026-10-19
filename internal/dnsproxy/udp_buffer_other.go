//go:build !linux

package dnsproxy

import "net"

func setUDPReadBuffer(conn *net.UDPConn, requested int) (int, error) {
	return 0, conn.SetReadBuffer(requested)
}
