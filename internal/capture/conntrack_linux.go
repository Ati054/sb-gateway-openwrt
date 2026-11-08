//go:build linux

package capture

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type ctTuple struct {
	source, target netip.Addr
	sport, dport   uint16
	protocol       byte
}
type ctEntry struct {
	original, reply ctTuple
	status          uint32
	identity        bool
	deletion        []byte
}
type ctScope struct {
	local, ingress map[netip.Addr]bool
	clients        []netip.Prefix
	port           uint16
}

// Netlink attribute lengths exclude alignment padding; their headers are native
// endian, while addresses, ports, status and the conntrack ID are network endian.
func ctAttributes(data []byte, visit func(uint16, []byte, []byte)) bool {
	for len(data) > 0 {
		if len(data) < 4 {
			return false
		}
		size := int(binary.NativeEndian.Uint16(data))
		aligned := (size + 3) &^ 3
		if size < 4 || aligned > len(data) {
			return false
		}
		visit(binary.NativeEndian.Uint16(data[2:])&0x3fff, data[4:size], data[:aligned])
		data = data[aligned:]
	}
	return true
}

func ctReadTuple(data []byte) (tuple ctTuple, valid bool) {
	valid = ctAttributes(data, func(kind uint16, value, raw []byte) {
		switch kind {
		case 1:
			if !ctAttributes(value, func(kind uint16, value, raw []byte) {
				var ip netip.Addr
				if len(value) == 4 && (kind == 1 || kind == 2) {
					ip = netip.AddrFrom4([4]byte(value))
				}
				if len(value) == 16 && (kind == 3 || kind == 4) {
					ip = netip.AddrFrom16([16]byte(value))
				}
				if kind == 1 || kind == 3 {
					tuple.source = ip.Unmap()
				}
				if kind == 2 || kind == 4 {
					tuple.target = ip.Unmap()
				}
			}) {
				tuple = ctTuple{}
			}
		case 2:
			if !ctAttributes(value, func(kind uint16, value, raw []byte) {
				if kind == 1 && len(value) == 1 {
					tuple.protocol = value[0]
				}
				if kind == 2 && len(value) == 2 {
					tuple.sport = binary.BigEndian.Uint16(value)
				}
				if kind == 3 && len(value) == 2 {
					tuple.dport = binary.BigEndian.Uint16(value)
				}
			}) {
				tuple = ctTuple{}
			}
		}
	})
	valid = valid && tuple.source.IsValid() && tuple.target.IsValid() && tuple.source.BitLen() == tuple.target.BitLen() && tuple.sport != 0 && tuple.dport != 0 && (tuple.protocol == 6 || tuple.protocol == 17)
	return
}

func ctReadEntry(data []byte) (entry ctEntry, valid bool) {
	if len(data) < 4 || (data[0] != unix.AF_INET && data[0] != unix.AF_INET6) {
		return entry, false
	}
	var original, reply bool
	entry.deletion = append([]byte(nil), data[:4]...)
	valid = ctAttributes(data[4:], func(kind uint16, value, raw []byte) {
		switch kind {
		case 1:
			entry.original, original = ctReadTuple(value)
			entry.deletion = append(entry.deletion, raw...)
		case 2:
			entry.reply, reply = ctReadTuple(value)
		case 3:
			if len(value) == 4 {
				entry.status = binary.BigEndian.Uint32(value)
			}
		case 12:
			if len(value) == 4 {
				entry.identity = true
				entry.deletion = append(entry.deletion, raw...)
			}
		case 18:
			if len(value) == 2 {
				entry.deletion = append(entry.deletion, raw...)
			}
		}
	})
	return entry, valid && original && reply && entry.identity
}

func (scope ctScope) matches(entry ctEntry) bool {
	a, b := entry.original, entry.reply
	if entry.status&(1<<5) == 0 || a.dport != 53 || b.sport != scope.port || a.protocol != b.protocol ||
		a.source != b.target || a.sport != b.dport || scope.local[a.target] || !scope.ingress[b.source] {
		return false
	}
	for _, prefix := range scope.clients {
		if prefix.Contains(a.source) {
			return true
		}
	}
	return false
}

// PruneLegacyDNS removes only old REDIRECT mappings into this service's DNS
// port, for this LAN's clients and external original destinations. Router-local
// DNS, ordinary TCP/UDP sessions and other services' redirects are retained.
// Tuple + kernel connection ID + zone prevent deleting a reused connection.
func PruneLegacyDNS(ctx context.Context, iface string, port int, additional []string) (int, error) {
	if !interfaceName.MatchString(iface) || port < 1024 || port > 65535 || len(additional) > 256 {
		return 0, errors.New("invalid legacy DNS cleanup scope")
	}
	scope := ctScope{local: map[netip.Addr]bool{}, ingress: map[netip.Addr]bool{}, port: uint16(port)}
	interfaces, err := net.Interfaces()
	if err != nil {
		return 0, err
	}
	found := false
	for _, item := range interfaces {
		addresses, err := item.Addrs()
		if err != nil {
			return 0, err
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil {
				continue
			}
			ip := prefix.Addr().Unmap()
			scope.local[ip] = true
			if item.Name == iface {
				found = true
				scope.ingress[ip] = true
				scope.clients = append(scope.clients, prefix.Masked())
			}
		}
	}
	if !found {
		return 0, errors.New("LAN addresses unavailable for legacy DNS cleanup")
	}
	for _, raw := range additional {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return 0, err
		}
		scope.clients = append(scope.clients, prefix.Masked())
	}
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_NETFILTER)
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)
	if err = unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, err
	}
	if err = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Usec: 200000}); err != nil {
		return 0, err
	}
	sequence := uint32(1)
	send := func(kind uint16, flags uint16, data []byte) error {
		message := make([]byte, 16+len(data))
		binary.NativeEndian.PutUint32(message, uint32(len(message)))
		binary.NativeEndian.PutUint16(message[4:], kind)
		binary.NativeEndian.PutUint16(message[6:], flags)
		binary.NativeEndian.PutUint32(message[8:], sequence)
		copy(message[16:], data)
		return unix.Sendto(fd, message, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK})
	}
	packet := make([]byte, 64<<10)
	receive := func() ([]syscall.NetlinkMessage, error) {
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n, from, err := unix.Recvfrom(fd, packet, 0)
			if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				continue
			}
			if err != nil {
				return nil, err
			}
			peer, ok := from.(*unix.SockaddrNetlink)
			if !ok || peer.Pid != 0 {
				return nil, errors.New("unexpected conntrack sender")
			}
			return syscall.ParseNetlinkMessage(packet[:n])
		}
	}
	if err = send(0x101, unix.NLM_F_REQUEST|unix.NLM_F_DUMP, []byte{unix.AF_UNSPEC, 0, 0, 0}); err != nil {
		return 0, err
	}
	var pending [][]byte
	done := false
	for !done {
		messages, err := receive()
		if err != nil {
			return 0, err
		}
		for _, message := range messages {
			if message.Header.Seq != sequence {
				continue
			}
			if message.Header.Type == unix.NLMSG_ERROR {
				if err = ctNetlinkError(message.Data); err != nil {
					return 0, err
				}
				continue
			}
			if message.Header.Type == unix.NLMSG_DONE {
				if message.Header.Flags&unix.NLM_F_DUMP_INTR != 0 {
					return 0, errors.New("conntrack snapshot interrupted")
				}
				done = true
				continue
			}
			if message.Header.Type != 0x100 {
				continue
			}
			entry, valid := ctReadEntry(message.Data)
			if valid && scope.matches(entry) {
				pending = append(pending, entry.deletion)
			}
		}
	}
	removed := 0
	for _, data := range pending {
		sequence++
		if err = send(0x102, unix.NLM_F_REQUEST|unix.NLM_F_ACK, data); err != nil {
			return removed, err
		}
		acknowledged := false
		for !acknowledged {
			messages, err := receive()
			if err != nil {
				return removed, err
			}
			for _, message := range messages {
				if message.Header.Seq != sequence || message.Header.Type != unix.NLMSG_ERROR {
					continue
				}
				err = ctNetlinkError(message.Data)
				if err != nil && !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ESRCH) {
					return removed, err
				}
				if err == nil {
					removed++
				}
				acknowledged = true
			}
		}
	}
	return removed, nil
}

func ctNetlinkError(data []byte) error {
	if len(data) < 4 {
		return errors.New("truncated conntrack acknowledgment")
	}
	code := int32(binary.NativeEndian.Uint32(data))
	if code == 0 {
		return nil
	}
	if code > 0 {
		return fmt.Errorf("invalid conntrack acknowledgment")
	}
	return syscall.Errno(-code)
}

// Keep a common bounded deadline at the CLI boundary, independently of the
// firewall helper's process deadline and of the current number of LAN clients.
const LegacyDNSCleanupTimeout = 5 * time.Second
