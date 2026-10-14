package subscription

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

var errStructuredUnsupported = errors.New("unsupported structured proxy settings")

// Only connection entries are imported. Provider routing, DNS, inbounds and
// scripts must never replace this router's independently configured policy.
func structuredSubscription(body []byte, maxNodes int, validate func(map[string]any) error) ([]map[string]any, ParseStats, bool, error) {
	text := bytes.TrimPrefix(bytes.TrimSpace(body), []byte("\xef\xbb\xbf"))
	stats := ParseStats{}
	isJSON := bytes.HasPrefix(text, []byte("{")) || bytes.HasPrefix(text, []byte("["))
	isYAML := !isJSON && (bytes.HasPrefix(text, []byte("proxies:")) || bytes.Contains(text, []byte("\nproxies:")))
	if !isJSON && !isYAML {
		return nil, stats, false, nil
	}
	nodes := make([]map[string]any, 0, min(maxNodes, 64))
	appendNode := func(raw map[string]any) {
		if len(nodes) >= maxNodes {
			stats.Limited++
			return
		}
		links, err := structuredLinks(raw)
		if errors.Is(err, errStructuredUnsupported) {
			stats.Unsupported++
			return
		}
		if err != nil {
			stats.Invalid++
			return
		}
		for _, link := range links {
			if len(nodes) >= maxNodes {
				stats.Limited++
				continue
			}
			parsed, _, err := parseProxySubscriptionValidated([]byte(link), 1, validate)
			if err != nil || len(parsed) != 1 {
				stats.Invalid++
				continue
			}
			nodes = append(nodes, parsed[0])
		}
	}
	var err error
	if isJSON {
		decoder := json.NewDecoder(bytes.NewReader(text))
		decoder.UseNumber()
		first, e := decoder.Token()
		if e != nil {
			return nil, stats, true, errors.New("Invalid JSON subscription")
		}
		readArray := func() error {
			for decoder.More() {
				var raw json.RawMessage
				if err := decoder.Decode(&raw); err != nil {
					return err
				}
				if len(raw) > 64<<10 {
					stats.Invalid++
					continue
				}
				var value map[string]any
				d := json.NewDecoder(bytes.NewReader(raw))
				d.UseNumber()
				if err := d.Decode(&value); err != nil || value == nil {
					stats.Invalid++
					continue
				}
				appendNode(value)
			}
			_, err := decoder.Token()
			return err
		}
		switch first {
		case json.Delim('['):
			err = readArray()
		case json.Delim('{'):
			for decoder.More() {
				key, e := decoder.Token()
				if e != nil {
					err = e
					break
				}
				if key == "outbounds" || key == "proxies" {
					token, e := decoder.Token()
					if e != nil || token != json.Delim('[') {
						err = errors.New("proxy entries must be an array")
						break
					}
					if err = readArray(); err != nil {
						break
					}
				} else {
					var ignored json.RawMessage
					if err = decoder.Decode(&ignored); err != nil {
						break
					}
				}
			}
			if err == nil {
				_, err = decoder.Token()
			}
		default:
			err = errors.New("invalid root")
		}
		if err == nil {
			var extra json.RawMessage
			if decoder.Decode(&extra) != io.EOF {
				err = errors.New("trailing JSON content")
			}
		}
	} else {
		// YAML decoding constructs a tree; use a tighter bound than streamed JSON.
		if len(body) > 2<<20 {
			return nil, stats, true, errors.New("YAML subscription exceeds the size limit")
		}

		var document yaml.Node
		err = yaml.Unmarshal(body, &document)
		if err == nil {
			var proxies *yaml.Node
			if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
				err = errors.New("invalid YAML root")
			} else {
				root := document.Content[0]
				for i := 0; i+1 < len(root.Content); i += 2 {
					if root.Content[i].Value == "proxies" {
						if proxies != nil {
							err = errors.New("duplicate proxies key")
							break
						}
						proxies = root.Content[i+1]
					}
				}
			}
			if err == nil && proxies != nil {
				if proxies.Kind != yaml.SequenceNode {
					err = errors.New("proxy entries must be an array")
				} else {
					for _, entry := range proxies.Content {
						if len(nodes) >= maxNodes {
							stats.Limited++
							continue
						}
						count := 0
						if !boundedYAMLNode(entry, 0, &count, map[*yaml.Node]bool{}) {
							stats.Invalid++
							continue
						}
						var raw map[string]any
						if e := entry.Decode(&raw); e != nil || raw == nil {
							stats.Invalid++
							continue
						}
						appendNode(raw)
					}
				}
			}
		}
	}
	if err != nil {
		return nil, stats, true, errors.New("Invalid structured subscription")
	}
	if len(nodes) == 0 {
		return nil, stats, true, errors.New("Subscription contains no valid supported proxy nodes.")
	}
	deduplicateSubscriptionNodeIDs(nodes)
	return nodes, stats, true, nil
}

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func textValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return ""
}
func value(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := textValue(m[key]); v != "" {
			return v
		}
	}
	return ""
}
func on(v any) bool { return queryFlag(textValue(v)) }
func listValue(v any) string {
	if a, ok := v.([]any); ok {
		parts := []string{}
		for _, x := range a {
			parts = append(parts, textValue(x))
		}
		return strings.Join(parts, ",")
	}
	return textValue(v)
}
func put(q url.Values, key, v string) {
	if v != "" {
		q.Set(key, v)
	}
}

func structuredLinks(raw map[string]any) ([]string, error) {
	if value(raw, "protocol") != "" {
		return xrayStructuredLinks(raw)
	}
	kind := strings.ToLower(value(raw, "type"))
	if kind == "ss" {
		kind = "shadowsocks"
	}
	if kind != "vless" && kind != "vmess" && kind != "trojan" && kind != "shadowsocks" && kind != "hysteria2" {
		return nil, errStructuredUnsupported
	}
	if value(raw, "detour", "dialer-proxy") != "" || on(object(raw["multiplex"])["enabled"]) || on(object(raw["smux"])["enabled"]) {
		return nil, errStructuredUnsupported
	}

	if len(object(raw["ech-opts"])) > 0 || value(raw, "client-cert", "client-key", "certificate", "certificate-path") != "" {
		return nil, errStructuredUnsupported
	}
	if value(raw, "packet-encoding") != "" && value(raw, "packet-encoding") != "xudp" {
		return nil, errStructuredUnsupported
	}
	q := url.Values{}
	q.Set("security", "none")
	if (kind == "trojan" && raw["server_port"] == nil) || kind == "hysteria2" {
		q.Set("security", "tls")
	}
	tls := object(raw["tls"])
	if tls != nil {
		if on(tls["enabled"]) {
			q.Set("security", "tls")
		}
		put(q, "sni", value(tls, "server_name"))
		if on(tls["insecure"]) {
			q.Set("allowInsecure", "1")
		}
		put(q, "alpn", listValue(tls["alpn"]))
		put(q, "fp", value(object(tls["utls"]), "fingerprint"))
		if len(object(tls["ech"])) > 0 && on(object(tls["ech"])["enabled"]) {
			return nil, errStructuredUnsupported
		}
		if tls["certificate_public_key_sha256"] != nil || tls["certificate"] != nil || tls["certificate_path"] != nil {
			return nil, errStructuredUnsupported
		}
		reality := object(tls["reality"])
		if on(reality["enabled"]) {
			q.Set("security", "reality")
			put(q, "pbk", value(reality, "public_key"))
			put(q, "sid", value(reality, "short_id"))
		}
	} else {
		if raw["tls"] != nil && !on(raw["tls"]) {
			q.Set("security", "none")
		}
		if on(raw["tls"]) {
			q.Set("security", "tls")
		}
		put(q, "sni", value(raw, "servername", "sni"))
		put(q, "fp", value(raw, "client-fingerprint"))
		put(q, "alpn", listValue(raw["alpn"]))
		if on(raw["skip-cert-verify"]) {
			q.Set("allowInsecure", "1")
		}
		reality := object(raw["reality-opts"])
		if len(reality) > 0 {
			q.Set("security", "reality")
			put(q, "pbk", value(reality, "public-key"))
			put(q, "sid", value(reality, "short-id"))
		}
	}
	put(q, "pcs", listValue(raw["pinnedPeerCertSha256"]))
	if kind == "vless" && value(raw, "encryption") != "" && value(raw, "encryption") != "none" {
		return nil, errStructuredUnsupported
	}
	put(q, "flow", value(raw, "flow"))
	put(q, "encryption", value(raw, "security", "cipher"))
	put(q, "alterId", value(raw, "alter_id", "alterId"))
	transport := object(raw["transport"])
	network := value(transport, "type")
	if network == "" && tls == nil {
		network = value(raw, "network")
	}
	if network == "" {
		network = "tcp"
	}
	if network == "raw" {
		network = "tcp"
	}
	q.Set("type", network)
	options := transport
	if len(options) == 0 {
		options = object(raw[network+"-opts"])
	}

	for _, key := range []string{"max_early_data", "max-early-data", "early_data_header_name", "early-data-header-name"} {
		if value(options, key) != "" && value(options, key) != "0" {
			return nil, errStructuredUnsupported
		}
	}
	for key := range object(options["headers"]) {
		if !strings.EqualFold(key, "Host") {
			return nil, errStructuredUnsupported
		}
	}
	if network == "xhttp" && len(object(options["extra"])) > 0 {
		return nil, errStructuredUnsupported
	}
	put(q, "path", value(options, "path"))
	put(q, "serviceName", value(options, "service_name", "grpc-service-name"))
	put(q, "host", value(object(options["headers"]), "Host", "host"))
	if q.Get("host") == "" {
		put(q, "host", listValue(options["host"]))
	}
	if network == "xhttp" {
		put(q, "mode", value(options, "mode"))
	}
	if kind == "hysteria2" {
		if tls != nil && !on(tls["enabled"]) {
			return nil, errors.New("Hysteria2 requires TLS")
		}
		obfs := object(raw["obfs"])
		if obfs != nil {
			put(q, "obfs", value(obfs, "type"))
			put(q, "obfs-password", value(obfs, "password"))
		} else {
			put(q, "obfs", value(raw, "obfs"))
			put(q, "obfs-password", value(raw, "obfs-password"))
		}
		put(q, "upmbps", value(raw, "up_mbps"))
		put(q, "downmbps", value(raw, "down_mbps"))
	}
	password := value(raw, "password", "auth")
	credential := value(raw, "uuid")
	if kind == "trojan" || kind == "hysteria2" {
		credential = password
	}
	scheme := kind
	user := url.User(credential)
	if kind == "shadowsocks" {
		scheme = "ss"
		user = url.UserPassword(value(raw, "method", "cipher"), password)
		put(q, "plugin", strings.TrimSuffix(value(raw, "plugin")+";"+value(raw, "plugin_opts", "plugin-opts"), ";"))
	}
	host := value(raw, "server")
	port := value(raw, "server_port", "port")
	if host == "" || port == "" {
		return nil, errors.New("missing endpoint")
	}
	u := url.URL{Scheme: scheme, User: user, Host: net.JoinHostPort(host, port), RawQuery: q.Encode(), Fragment: value(raw, "name", "tag")}
	return []string{u.String()}, nil
}

func xrayStructuredLinks(raw map[string]any) ([]string, error) {
	kind := strings.ToLower(value(raw, "protocol"))
	if kind == "hysteria" && value(object(raw["settings"]), "version") == "2" {
		kind = "hysteria2"
	}
	settings := object(raw["settings"])
	stream := object(raw["streamSettings"])
	if kind != "vless" && kind != "vmess" && kind != "trojan" && kind != "shadowsocks" && kind != "hysteria2" {
		return nil, errStructuredUnsupported
	}
	if value(object(raw["proxySettings"]), "tag") != "" || on(object(raw["mux"])["enabled"]) {
		return nil, errStructuredUnsupported
	}
	entries, _ := settings["servers"].([]any)
	if kind == "vless" || kind == "vmess" {
		entries, _ = settings["vnext"].([]any)
	}

	if len(entries) == 0 && value(settings, "address") != "" {
		entries = []any{settings}
	}
	security := value(stream, "security")
	if security != "" && security != "none" && security != "tls" && security != "reality" {
		return nil, errStructuredUnsupported
	}
	tlsSettings := object(stream[security+"Settings"])
	if tlsSettings["certificates"] != nil || on(tlsSettings["enableSessionResumption"]) || value(tlsSettings, "echConfigList") != "" {
		return nil, errStructuredUnsupported
	}
	links := []string{}
	for _, entry := range entries {
		server := object(entry)
		users, _ := server["users"].([]any)
		if len(users) == 0 || kind == "trojan" || kind == "shadowsocks" || kind == "hysteria2" {
			users = []any{server}
		}
		for _, account := range users {
			user := object(account)
			if kind == "vless" && value(user, "encryption") != "" && value(user, "encryption") != "none" {
				links = append(links, "")
				continue
			}
			node := map[string]any{"type": kind, "server": server["address"], "port": server["port"], "uuid": user["id"], "password": user["password"], "method": user["method"], "security": user["security"], "alterId": user["alterId"], "flow": user["flow"], "tag": raw["tag"], "network": stream["network"]}
			security := value(stream, "security")
			tls := object(stream[security+"Settings"])
			if security != "" && security != "none" && security != "tls" && security != "reality" {
				return nil, errStructuredUnsupported
			}
			node["tls"] = false
			if security == "tls" || security == "reality" {
				node["tls"] = true
				node["sni"] = tls["serverName"]
				node["client-fingerprint"] = tls["fingerprint"]
				node["alpn"] = tls["alpn"]
				node["skip-cert-verify"] = tls["allowInsecure"]
				node["pinnedPeerCertSha256"] = tls["pinnedPeerCertSha256"]
			}
			if security == "reality" {
				node["reality-opts"] = map[string]any{"public-key": firstNonEmpty(value(tls, "password"), value(tls, "publicKey")), "short-id": tls["shortId"]}
			}
			switch value(stream, "network") {
			case "ws":
				node["ws-opts"] = object(stream["wsSettings"])
			case "grpc":
				g := object(stream["grpcSettings"])
				node["grpc-opts"] = map[string]any{"grpc-service-name": g["serviceName"]}
			case "httpupgrade":
				node["httpupgrade-opts"] = object(stream["httpupgradeSettings"])
			case "xhttp":
				node["xhttp-opts"] = object(stream["xhttpSettings"])
			}

			if kind == "hysteria2" {
				h := object(stream["hysteriaSettings"])
				node["password"] = h["auth"]
				node["network"] = "tcp"
				udp, _ := object(stream["finalmask"])["udp"].([]any)
				if len(udp) > 1 {
					return nil, errStructuredUnsupported
				}
				if len(udp) == 1 {
					mask := object(udp[0])
					if value(mask, "type") != "salamander" {
						return nil, errStructuredUnsupported
					}
					node["obfs"] = "salamander"
					node["obfs-password"] = object(mask["settings"])["password"]
				}
			} else if len(object(stream["finalmask"])) > 0 {
				return nil, errStructuredUnsupported
			}
			converted, err := structuredLinks(node)
			if err != nil {
				if errors.Is(err, errStructuredUnsupported) {
					return nil, err
				}
				links = append(links, "") // Count this bad endpoint; retain other accounts.
				continue
			}
			links = append(links, converted...)
		}
	}
	if len(links) == 0 {
		return nil, errors.New("missing outbound accounts")
	}
	return links, nil
}

// Check aliases before decoding a proxy map: expansion and recursion are bounded
// independently of the input's byte size. Ordinary shared Clash defaults work.
func boundedYAMLNode(node *yaml.Node, depth int, count *int, stack map[*yaml.Node]bool) bool {
	if node == nil || depth > 32 || stack[node] {
		return false
	}
	*count++
	if *count > 2048 {
		return false
	}
	stack[node] = true
	defer delete(stack, node)
	if node.Kind == yaml.AliasNode {
		return boundedYAMLNode(node.Alias, depth+1, count, stack)
	}
	for _, child := range node.Content {
		if !boundedYAMLNode(child, depth+1, count, stack) {
			return false
		}
	}
	return true
}
