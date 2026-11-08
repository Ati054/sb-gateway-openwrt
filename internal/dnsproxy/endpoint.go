package dnsproxy

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Endpoint describes one DNS upstream. Plain UDP is intentionally allowed
// only on the direct-WAN path; the local Xray HTTP inbounds carry TCP streams.
type Endpoint struct {
	Protocol    string
	URL         string
	Address     string
	ServerName  string
	Interface   string // Selected netifd uplink, only for ISP DNS.
	WANIdentity string // Addresses and default routes that identify this uplink generation.
}

func ParseEndpoint(raw string, direct bool) (Endpoint, error) {
	if raw == WANResolverURL {
		if !direct {
			return Endpoint{}, errors.New("WAN DNS is available only on the WAN path")
		}
		return Endpoint{Protocol: "wan", URL: raw}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || u.Opaque != "" {
		return Endpoint{}, errors.New("invalid DNS upstream address")
	}
	result := Endpoint{URL: raw, ServerName: u.Hostname()}
	var defaultPort string
	switch u.Scheme {
	case "https":
		if u.Path == "" {
			return Endpoint{}, errors.New("DoH address needs a path")
		}
		result.Protocol = "doh"
		defaultPort = "443"
	case "tls":
		result.Protocol = "dot"
		defaultPort = "853"
	case "tcp":
		result.Protocol = "tcp"
		defaultPort = "53"
	case "udp":
		if !direct {
			return Endpoint{}, errors.New("plain UDP is supported only through WAN")
		}
		result.Protocol = "udp"
		defaultPort = "53"
	case "dns":
		if !direct {
			return Endpoint{}, errors.New("automatic UDP/TCP DNS is supported only through WAN")
		}
		result.Protocol = "dns"
		defaultPort = "53"
	default:
		return Endpoint{}, fmt.Errorf("unsupported DNS protocol %q", u.Scheme)
	}
	if result.Protocol != "doh" && (u.Path != "" || u.RawPath != "") {
		return Endpoint{}, errors.New("DNS upstream path is not allowed")
	}
	if result.Protocol == "tcp" || result.Protocol == "udp" || result.Protocol == "dns" {
		if _, err := netip.ParseAddr(result.ServerName); err != nil {
			return Endpoint{}, errors.New("plain DNS upstream must be an IP address")
		}
	} else if strings.ContainsAny(result.ServerName, " \t\r\n/") {
		return Endpoint{}, errors.New("invalid DNS upstream hostname")
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return Endpoint{}, errors.New("invalid DNS upstream port")
	}
	result.Address = net.JoinHostPort(result.ServerName, port)
	return result, nil
}
