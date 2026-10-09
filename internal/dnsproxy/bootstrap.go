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
func BootstrapDirect(ctx context.Context, raw string) (string, error) {
	endpoint, err := ParseEndpoint(raw, true)
	if err != nil {
		return "", err
	}
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
	query := []byte{0x72, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0,
		7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
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
