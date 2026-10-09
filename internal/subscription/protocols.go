package subscription

import (
	"encoding/base64"
	"encoding/json"
	"example.org/resilient-proxy/internal/sip003"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func invalidProtocol(protocol string) error {
	return &subscriptionParseError{code: "invalid_node", message: protocol + " subscription node has invalid or unsupported connection settings."}
}

// Reuse the existing endpoint, TLS verification and transport validation.
// The synthetic UUID never enters the resulting node or credential store.
func sharedStreamNode(protocol, host string, query url.Values, label string, index int) (map[string]any, error) {
	if security := query.Get("security"); security != "" && security != "tls" && security != "none" {
		return nil, invalidProtocol(protocol)
	}
	if query.Get("flow") != "" {
		return nil, invalidProtocol(protocol)
	}
	u := &url.URL{Scheme: "vless", User: url.User("11111111-1111-1111-1111-111111111111"), Host: host, RawQuery: query.Encode(), Fragment: label}
	node, _, err := parseProviderVLESSLink(u.String(), "node")
	if err != nil {
		return nil, invalidProtocol(protocol)
	}
	delete(node, "subscription_reserve_id")
	delete(node, "subscription_id")
	node["protocol"] = strings.ToLower(protocol)
	decorateSubscriptionNode(node, u.String(), index)
	return node, nil
}

func validPassword(password string) bool {
	return password != "" && len(password) <= 4096 && !strings.ContainsAny(password, "\r\n\x00")
}

func parseSubscriptionTrojanLink(link string, index int) (map[string]any, error) {
	u, err := url.Parse(link)
	if err != nil || u.User == nil {
		return nil, invalidProtocol("Trojan")
	}
	password := u.User.Username()
	if suffix, ok := u.User.Password(); ok {
		password += ":" + suffix
	}
	if !validPassword(password) {
		return nil, invalidProtocol("Trojan")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, invalidProtocol("Trojan")
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "443")
	}
	if q.Get("security") == "" {
		q.Set("security", "tls")
	}
	node, err := sharedStreamNode("Trojan", u.Host, q, u.Fragment, index)
	if err != nil {
		return nil, err
	}
	node["_password"] = password
	setSubscriptionNodeID(node, index)
	return node, nil
}

func parseSubscriptionVMessLink(link string, index int) (map[string]any, error) {
	payload := link[len("vmess://"):]
	// URI-form VMess shares use UUID userinfo and the same transport parameters.
	if strings.Contains(payload, "@") {
		u, err := url.Parse(link)
		if err != nil || u.User == nil {
			return nil, invalidProtocol("VMess")
		}
		uuid := strings.ToLower(u.User.Username())
		if _, has := u.User.Password(); has || !reserveUUIDPattern.MatchString(uuid) {
			return nil, invalidProtocol("VMess")
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return nil, invalidProtocol("VMess")
		}
		node, err := sharedStreamNode("VMess", u.Host, q, u.Fragment, index)
		if err != nil {
			return nil, err
		}
		security := firstNonEmpty(q.Get("encryption"), "auto")
		if !validVMessSecurity(security) || (q.Get("alterId") != "" && q.Get("alterId") != "0") {
			return nil, invalidProtocol("VMess")
		}
		node["_uuid"], node["security"] = uuid, security
		setSubscriptionNodeID(node, index)
		return node, nil
	}
	decoded, err := decodeSubscriptionBase64(payload)
	if err != nil || len(decoded) > 64<<10 {
		return nil, invalidProtocol("VMess")
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(decoded, &raw) != nil {
		return nil, invalidProtocol("VMess")
	}
	get := func(key string) string {
		var text string
		if json.Unmarshal(raw[key], &text) == nil {
			return text
		}
		var number json.Number
		if json.Unmarshal(raw[key], &number) == nil {
			return number.String()
		}
		return ""
	}
	uuid := strings.ToLower(get("id"))
	host := get("add")
	port, err := strconv.Atoi(get("port"))
	if err != nil || port < 1 || port > 65535 || !validReserveHost(host) || !reserveUUIDPattern.MatchString(uuid) {
		return nil, invalidProtocol("VMess")
	}
	// The bundled core supports VMess AEAD, not obsolete nonzero alterId.
	if aid := get("aid"); aid != "" && aid != "0" {
		return nil, invalidProtocol("VMess")
	}
	security := firstNonEmpty(get("scy"), "auto")
	if !validVMessSecurity(security) {
		return nil, invalidProtocol("VMess")
	}
	q := url.Values{}
	q.Set("type", firstNonEmpty(get("net"), "tcp"))
	for _, pair := range [][2]string{{"host", "host"}, {"path", "path"}, {"sni", "sni"}, {"alpn", "alpn"}, {"fp", "fp"}, {"insecure", "insecure"}, {"allowInsecure", "allowInsecure"}, {"pcs", "pcs"}, {"vcn", "vcn"}, {"country", "country"}, {"city", "city"}} {
		if v := get(pair[0]); v != "" {
			q.Set(pair[1], v)
		}
	}
	if q.Get("type") == "grpc" {
		q.Set("serviceName", get("path"))
	}
	if tls := get("tls"); tls != "" {
		q.Set("security", tls)
	}
	header := get("type")
	if header != "" && header != "none" && !(q.Get("type") == "tcp" && header == "http") {
		return nil, invalidProtocol("VMess")
	}
	node, err := sharedStreamNode("VMess", net.JoinHostPort(host, strconv.Itoa(port)), q, get("ps"), index)
	if err != nil {
		return nil, err
	}
	if header == "http" {
		node["transport"] = map[string]any{"type": "tcp", "header_type": "http", "host": get("host"), "path": firstNonEmpty(get("path"), "/")}
	}
	node["_uuid"], node["security"] = uuid, security
	setSubscriptionNodeID(node, index)
	return node, nil
}

func validVMessSecurity(value string) bool {
	return value == "auto" || value == "aes-128-gcm" || value == "chacha20-poly1305"
}

func parseSubscriptionSSLink(link string, index int) (map[string]any, error) {
	// Normalize encoded credentials before URL parsing, including legacy
	// whole-authority base64 and older standard-base64 userinfo containing '/'.
	rest, fragment, _ := strings.Cut(link[len("ss://"):], "#")
	if !strings.Contains(rest, "@") {
		decoded, err := decodeSubscriptionBase64(rest)
		if err != nil {
			return nil, invalidProtocol("Shadowsocks")
		}
		rest = string(decoded)
	}
	at := strings.LastIndex(rest, "@")
	if at < 1 {
		return nil, invalidProtocol("Shadowsocks")
	}
	auth, err := url.PathUnescape(rest[:at])
	if err != nil {
		return nil, invalidProtocol("Shadowsocks")
	}
	method, password, plain := strings.Cut(auth, ":")
	if !plain {
		decoded, err := decodeSubscriptionBase64(auth)
		if err != nil {
			return nil, invalidProtocol("Shadowsocks")
		}
		method, password, plain = strings.Cut(string(decoded), ":")
	}
	if !plain {
		return nil, invalidProtocol("Shadowsocks")
	}
	u, err := url.Parse("ss://" + rest[at+1:])
	if err != nil || u.User != nil || u.Path != "" && u.Path != "/" {
		return nil, invalidProtocol("Shadowsocks")
	}
	u.User = url.UserPassword(method, password)
	label, err := url.PathUnescape(fragment)
	if err != nil {
		return nil, invalidProtocol("Shadowsocks")
	}
	u.Fragment = label
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, invalidProtocol("Shadowsocks")
	}
	method = strings.ToLower(method)
	if !plain || !validPassword(password) || !validSSMethod(method, password) {
		return nil, invalidProtocol("Shadowsocks")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || !validReserveHost(u.Hostname()) {
		return nil, invalidProtocol("Shadowsocks")
	}
	node := map[string]any{"protocol": "shadowsocks", "server": u.Hostname(), "server_port": port, "label": u.Fragment, "_password": password, "method": method}
	if value := q.Get("plugin"); value != "" {
		plugin, err := sip003.Parse(value)
		if err != nil {
			return nil, invalidProtocol("Shadowsocks plugin")
		}
		node["plugin"] = map[string]any{"name": plugin.Name, "options": plugin.Options}
	}
	decorateSubscriptionNode(node, u.String(), index)
	return node, nil
}

func validSSMethod(method, password string) bool {
	switch method {
	case "aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305", "chacha20-poly1305", "xchacha20-ietf-poly1305", "xchacha20-poly1305":
		return true
	case "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		size := 32
		if method == "2022-blake3-aes-128-gcm" {
			size = 16
		}
		for _, key := range strings.Split(password, ":") {
			decoded, err := base64.StdEncoding.DecodeString(key)
			if err != nil || len(decoded) != size {
				return false
			}
		}
		return true
	}
	return false
}
