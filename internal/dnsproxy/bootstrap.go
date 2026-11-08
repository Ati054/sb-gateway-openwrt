package dnsproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// BootstrapDirect pins an independently resolved direct DNS endpoint before
// dnsmasq is pointed at this process. This prevents a recursive DoH lookup.
// It also proves the chosen WAN resolver can answer without Xray.
type DirectBootstrap struct {
	IP             string
	upstream       string
	wan            *wanDNSSnapshot
	tcpPreferred   bool
	transportKnown bool
	transportRTT   time.Duration
}

func directProbeQuery() []byte {
	return []byte{0x72, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0,
		7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
}

func BootstrapDirect(ctx context.Context, raw string) (string, error) {
	prepared, err := PreflightDirect(ctx, raw)
	return prepared.IP, err
}

// Preserve the tested resolver and transport through DNS ownership changes.
// Otherwise the first real requests would repeat a failed UDP preflight.
func PreflightDirect(ctx context.Context, raw string) (DirectBootstrap, error) {
	if raw == WANResolverURL {
		state := &wanDNSState{lookup: discoverWANDNS}
		if err := state.refresh(ctx); err != nil {
			return DirectBootstrap{}, err
		}
		response, err := exchangeWANDNS(ctx, state.current.Load(), directProbeQuery())
		if err != nil {
			return DirectBootstrap{}, err
		}
		if len(response) < 12 || response[3]&15 != 0 {
			return DirectBootstrap{}, errors.New("WAN DNS preflight returned no valid answer")
		}
		return DirectBootstrap{upstream: raw, wan: state.current.Load()}, nil
	}
	var preferred dnsTransportPreference
	ip, err := bootstrapDirectEndpoint(ctx, raw, &preferred)
	return DirectBootstrap{IP: ip, upstream: raw, tcpPreferred: preferred.Load(), transportKnown: preferred.Known(), transportRTT: preferred.latency()}, err
}

func bootstrapDirectEndpoint(ctx context.Context, raw string, tcpPreferred *dnsTransportPreference) (string, error) {
	endpoint, err := ParseEndpoint(raw, true)
	if err != nil {
		return "", err
	}
	query := directProbeQuery()
	address, port, err := net.SplitHostPort(endpoint.Address)
	if err != nil {
		return "", err
	}
	if _, err := netip.ParseAddr(address); err != nil {
		lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		// /etc/resolv.conf on OpenWrt can point straight at the WAN DHCP
		// resolver even while dnsmasq and https-dns-proxy work correctly.
		// Resolve through the still-running local service before handoff.
		resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:53")
		}}
		ips, lookupErr := resolver.LookupNetIP(lookupCtx, "ip4", address)
		cancel()
		if lookupErr != nil || len(ips) == 0 {
			if lookupErr == nil {
				lookupErr = errors.New("no IPv4 address")
			}
			return "", fmt.Errorf("resolve direct DNS endpoint %s through router DNS: %w", address, lookupErr)
		}
		address = ips[0].String()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var response []byte
	if endpoint.Protocol == "doh" {
		transport := &http.Transport{ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(address, port))
			}}
		defer transport.CloseIdleConnections()
		response, err = exchangeDoH(probeCtx, &http.Client{Transport: transport, Timeout: 6 * time.Second}, raw, query)
	} else if endpoint.Protocol == "dns" {
		response, err = exchangeAutomaticDNS(probeCtx, endpoint, query, tcpPreferred)
	} else {
		endpoint.Address = net.JoinHostPort(address, port)
		response, err = exchangeWire(probeCtx, endpoint, query, true, 0)
	}
	if err != nil {
		return "", fmt.Errorf("direct DNS WAN preflight: %w", err)
	}
	if len(response) < 12 || !bytes.Equal(response[:2], query[:2]) || response[2]&0x80 == 0 ||
		response[3]&0x0f != 0 {
		return "", errors.New("direct DNS WAN preflight returned no valid answer")
	}
	return strings.Trim(address, "[]"), nil
}
