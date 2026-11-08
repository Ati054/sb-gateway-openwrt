package dnsproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const WANResolverURL = "wan://auto"

var errWANDNSUnavailable = errors.New("WAN connection has no usable DNS server")

type wanDNSSnapshot struct {
	servers   []Endpoint
	key       string
	preferred atomic.Uint32
}

type wanDNSState struct {
	lookup  func(context.Context) ([]Endpoint, error)
	current atomic.Pointer[wanDNSSnapshot]
}

// Only netifd's active physical uplinks with a default route supply ISP DNS.
// Router-local DNS, VPN interfaces and /etc/resolv.conf are never fallback
// sources: they may already lead back to this listener or a different route.
func parseWANDNS(body []byte, local map[netip.Addr]bool) ([]Endpoint, error) {
	type route struct {
		Target  string `json:"target"`
		Mask    int    `json:"mask"`
		Table   int    `json:"table"`
		Metric  int    `json:"metric"`
		Nexthop string `json:"nexthop"`
		Source  string `json:"source"`
	}
	type address struct {
		Address string `json:"address"`
		Mask    int    `json:"mask"`
	}
	type iface struct {
		Up     bool      `json:"up"`
		Name   string    `json:"interface"`
		Proto  string    `json:"proto"`
		Device string    `json:"l3_device"`
		DNS    []string  `json:"dns-server"`
		Routes []route   `json:"route"`
		IPv4   []address `json:"ipv4-address"`
		IPv6   []address `json:"ipv6-address"`
	}
	var dump struct {
		Interfaces []iface `json:"interface"`
	}
	if len(body) > 256*1024 || json.Unmarshal(body, &dump) != nil {
		return nil, errors.New("invalid WAN network status")
	}
	type candidate struct {
		info   iface
		metric int
	}
	var candidates []candidate
	for _, item := range dump.Interfaces {
		if !item.Up || item.Device == "" {
			continue
		}
		switch item.Proto {
		case "dhcp", "dhcpv6", "static", "pppoe", "ppp", "qmi", "mbim", "ncm", "wwan", "3g", "modemmanager":
		default:
			continue
		}
		virtual := false
		for _, prefix := range []string{"wg", "tun", "tap", "tailscale", "zt"} {
			if strings.HasPrefix(item.Device, prefix) {
				virtual = true
			}
		}
		if virtual {
			continue
		}
		best := -1
		for _, r := range item.Routes {
			if r.Mask == 0 && (r.Target == "0.0.0.0" || r.Target == "::") && (r.Table == 0 || r.Table == 254) && (best < 0 || r.Metric < best) {
				best = r.Metric
			}
		}
		if best >= 0 {
			candidates = append(candidates, candidate{item, best})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].metric < candidates[j].metric })
	seen := map[string]bool{}
	var result []Endpoint
	for _, c := range candidates {
		// Keep lease timers out of the identity. The same resolver address on
		// another WAN, a new source address or gateway must invalidate cache.
		identityBody, _ := json.Marshal(struct {
			Name, Device string
			IPv4, IPv6   []address
			Routes       []route
		}{c.info.Name, c.info.Device, c.info.IPv4, c.info.IPv6, c.info.Routes})
		identity := string(identityBody)
		for _, text := range c.info.DNS {
			ip, err := netip.ParseAddr(text)
			if err != nil {
				continue
			}
			ip = ip.Unmap()
			if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || local[ip.WithZone("")] {
				continue
			}
			if ip.Is6() && ip.IsLinkLocalUnicast() && ip.Zone() == "" {
				if c.info.Device == "" {
					continue
				}
				ip = ip.WithZone(c.info.Device)
			}
			key := ip.String() + "\x00" + identity
			if !seen[key] {
				seen[key] = true
				result = append(result, Endpoint{Protocol: "udp", Address: net.JoinHostPort(ip.String(), "53"), Interface: c.info.Device, WANIdentity: identity})
			}
		}
	}
	if len(result) == 0 {
		return nil, errWANDNSUnavailable
	}
	return result, nil
}

type wanStatusOutput struct{ body []byte }

func (w *wanStatusOutput) Write(p []byte) (int, error) {
	if len(w.body)+len(p) > 256*1024 {
		return 0, errors.New("WAN network status exceeds size limit")
	}
	w.body = append(w.body, p...)
	return len(p), nil
}

func discoverWANDNS(ctx context.Context) ([]Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/ubus", "call", "network.interface", "dump")
	command.WaitDelay = 200 * time.Millisecond
	var output wanStatusOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if command.Run() != nil {
		return nil, errors.New("cannot read WAN network status")
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, errors.New("cannot read local interface addresses")
	}
	local := map[netip.Addr]bool{}
	for _, raw := range addresses {
		if prefix, err := netip.ParsePrefix(raw.String()); err == nil {
			local[prefix.Addr().Unmap().WithZone("")] = true
		}
	}
	return parseWANDNS(output.body, local)
}

func (w *wanDNSState) refresh(ctx context.Context) error {
	servers, err := w.lookup(ctx)
	if err != nil && !errors.Is(err, errWANDNSUnavailable) {
		if w.current.Load() == nil {
			w.current.CompareAndSwap(nil, &wanDNSSnapshot{key: "unavailable"})
		}
		return err
	}
	next := &wanDNSSnapshot{key: "unavailable"}
	if err == nil {
		var identity strings.Builder
		for _, endpoint := range servers {
			host, _, splitErr := net.SplitHostPort(endpoint.Address)
			ip, parseErr := netip.ParseAddr(host)
			if splitErr != nil || parseErr != nil {
				continue
			}
			if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
				continue
			}
			endpoint.Protocol = "udp"
			next.servers = append(next.servers, endpoint)
			identity.WriteString(endpoint.Address + "\x00" + endpoint.Interface + "\x00" + endpoint.WANIdentity)
			identity.WriteByte('\n')
		}
		if len(next.servers) > 0 {
			sum := sha256.Sum256([]byte(identity.String()))
			next.key = hex.EncodeToString(sum[:16])
		}
	}
	if previous := w.current.Load(); previous != nil && previous.key == next.key {
		return nil
	}
	w.current.Store(next)
	return nil
}

func (w *wanDNSState) watch(ctx context.Context, changes <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-changes:
			if !ok {
				return
			}
			// Retry only this failed notification, never poll stable WAN state.
			// A transient ubus failure must not erase a working configuration.
			refreshed := false
			for _, delay := range []time.Duration{0, 50 * time.Millisecond, 200 * time.Millisecond, 750 * time.Millisecond, 2 * time.Second} {
				if delay > 0 {
					timer := time.NewTimer(delay)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
				if w.refresh(ctx) == nil {
					refreshed = true
					break
				}
			}
			if !refreshed && ctx.Err() == nil {
				w.current.Store(&wanDNSSnapshot{key: "unavailable"})
			}
		}
	}
}

func exchangeWANDNS(ctx context.Context, snapshot *wanDNSSnapshot, query []byte) ([]byte, error) {
	servers := snapshot.servers
	if len(servers) == 0 {
		return nil, errWANDNSUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var last error
	start := int(snapshot.preferred.Load()) % len(servers)
	for index := range servers {
		selected := (start + index) % len(servers)
		endpoint := servers[selected]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		deadline, _ := ctx.Deadline()
		attempt, cancelAttempt := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(servers)-index))
		body, err := exchangeWire(attempt, endpoint, query, true, 0)
		cancelAttempt()
		if err == nil && !matchingQuestion(query, body) {
			err = errDNSResponseMismatch
		}
		if err == nil {
			snapshot.preferred.Store(uint32(selected))
			return body, nil
		}
		last = err
	}
	return nil, last
}
