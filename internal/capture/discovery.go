package capture

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type AutomaticIP struct {
	IP     string `json:"ip"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
}
type Inventory struct {
	Complete   bool          `json:"-"`
	Interfaces []string      `json:"interfaces"`
	Addresses  []AutomaticIP `json:"addresses"`
}
type link struct {
	Name  string   `json:"ifname"`
	Flags []string `json:"flags"`
	Info  struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
}

// Read active kernel state and VPN sockets, never private keys or persisted configuration.
func Discover(ctx context.Context) Inventory {
	var links []link
	linkBody := command(ctx, "ip", "-j", "-d", "link", "show")
	if json.Unmarshal(linkBody, &links) != nil || len(links) == 0 {
		return Inventory{}
	}
	result, kinds := Inventory{Complete: true, Interfaces: []string{}, Addresses: []AutomaticIP{}}, map[string]string{}
	for _, l := range links {
		if !interfaceName.MatchString(l.Name) || !contains(l.Flags, "UP") {
			continue
		}
		kind := l.Info.Kind
		switch kind {
		case "wireguard":
			kind = "WireGuard"
		case "tun":
			kind = "VPN"
		case "xfrm", "vti", "vti6":
			kind = "IPsec"
		default:
			if _, err := os.Stat(filepath.Join("/sys/class/net", l.Name, "tun_flags")); err != nil {
				continue
			}
			kind = "VPN"
		}
		if strings.HasPrefix(l.Name, "tailscale") {
			kind = "Tailscale"
		}
		result.Interfaces = append(result.Interfaces, l.Name)
		kinds[l.Name] = kind
	}
	if len(kinds) > 0 {
		for _, family := range []string{"-4", "-6"} {
			result.Addresses = append(result.Addresses, routeAddresses(command(ctx, "ip", family, "-j", "route", "show", "table", "all"), kinds)...)
		}
	}
	if len(kinds) > 0 {
		if _, err := exec.LookPath("wg"); err == nil {
			peers := command(ctx, "wg", "show", "all", "endpoints")
			if peers == nil {
				result.Complete = false
			}
			result.Addresses = append(result.Addresses, wireGuardAddresses(peers, kinds)...)
		}
	}
	result.Addresses = append(result.Addresses, socketAddresses("/proc")...)
	result.Addresses = uniqueAddresses(result.Addresses)
	sort.Strings(result.Interfaces)
	return result
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// Bounded time and output keep optional discovery inexpensive on small routers.
func command(ctx context.Context, name string, args ...string) []byte {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &limitedOutput{Writer: &out, remaining: 1 << 20}
	if cmd.Run() != nil {
		return nil
	}
	return append([]byte{}, out.Bytes()...)
}

type limitedOutput struct {
	io.Writer
	remaining int
}

func (w *limitedOutput) Write(b []byte) (int, error) {
	if len(b) > w.remaining {
		return 0, io.ErrShortBuffer
	}
	n, err := w.Writer.Write(b)
	w.remaining -= n
	return n, err
}
func routeAddresses(body []byte, kinds map[string]string) []AutomaticIP {
	var routes []struct {
		Dst  string `json:"dst"`
		Dev  string `json:"dev"`
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &routes) != nil {
		return nil
	}
	var result []AutomaticIP
	for _, route := range routes {
		kind := kinds[route.Dev]
		if kind == "" || route.Type != "" && route.Type != "unicast" {
			continue
		}
		prefix, err := netip.ParsePrefix(route.Dst)
		if err != nil {
			if a, e := netip.ParseAddr(route.Dst); e == nil {
				prefix = netip.PrefixFrom(a, a.BitLen())
				err = nil
			}
		}
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			continue
		}
		result = append(result, AutomaticIP{prefix.Masked().String(), kind + " \u00b7 " + route.Dev, "route"})
	}
	return result
}
func wireGuardAddresses(body []byte, kinds map[string]string) []AutomaticIP {
	var result []AutomaticIP
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || kinds[f[0]] != "WireGuard" {
			continue
		}
		host, _, err := net.SplitHostPort(f[2])
		if err != nil {
			continue
		}
		a, err := netip.ParseAddr(host)
		if err != nil || a.IsUnspecified() || a.Zone() != "" {
			continue
		}
		result = append(result, AutomaticIP{a.Unmap().String(), "WireGuard \u00b7 " + f[0], "endpoint"})
	}
	return result
}
func uniqueAddresses(values []AutomaticIP) []AutomaticIP {
	result := []AutomaticIP{}
	seen := map[string]int{}
	for _, v := range values {
		if i, ok := seen[v.IP]; ok {
			if !strings.Contains(result[i].Source, v.Source) {
				result[i].Source += ", " + v.Source
			}
			continue
		}
		if len(result) >= 256 {
			break
		}
		seen[v.IP] = len(result)
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Source+result[i].IP < result[j].Source+result[j].IP })
	return result
}

// Match connected sockets by inode to known VPN daemons, never arbitrary installed packages.
func socketAddresses(root string) []AutomaticIP {
	inodes := map[string]string{}
	dirs, _ := os.ReadDir(root)
	for _, dir := range dirs {
		if _, err := strconv.Atoi(dir.Name()); err != nil {
			continue
		}
		base := filepath.Join(root, dir.Name())
		comm, _ := os.ReadFile(filepath.Join(base, "comm"))
		source := ""
		switch strings.TrimSpace(string(comm)) {
		case "openvpn":
			source = "OpenVPN"
		case "tailscaled":
			source = "Tailscale"
		case "zerotier-one":
			source = "ZeroTier"
		case "wireguard-go", "boringtun":
			source = "WireGuard"
		}
		if source == "" {
			continue
		}
		fds, _ := os.ReadDir(filepath.Join(base, "fd"))
		for i, fd := range fds {
			if i >= 256 {
				break
			}
			target, _ := os.Readlink(filepath.Join(base, "fd", fd.Name()))
			if strings.HasPrefix(target, "socket:[") {
				inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = source
			}
		}
	}
	var result []AutomaticIP
	if len(inodes) == 0 {
		return result
	}
	for _, table := range []string{"tcp", "udp", "tcp6", "udp6"} {
		file, err := os.Open(filepath.Join(root, "net", table))
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(file, 1<<20))
		file.Close()
		for _, line := range strings.Split(string(body), "\n") {
			f := strings.Fields(line)
			if len(f) < 10 || inodes[f[9]] == "" {
				continue
			}
			encoded, _, _ := strings.Cut(f[2], ":")
			a, ok := socketIP(encoded)
			if !ok || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
				continue
			}
			result = append(result, AutomaticIP{a.String(), inodes[f[9]], "endpoint"})
		}
	}
	return result
}
func socketIP(value string) (netip.Addr, bool) {
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != 4 && len(b) != 16 {
		return netip.Addr{}, false
	}
	for i := 0; i < len(b); i += 4 {
		v := binary.LittleEndian.Uint32(b[i : i+4])
		binary.BigEndian.PutUint32(b[i:i+4], v)
	}
	a, ok := netip.AddrFromSlice(b)
	return a.Unmap(), ok
}
