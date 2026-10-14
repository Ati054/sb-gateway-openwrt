package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

// RenderOutbound returns the legacy Xray configuration form required by
// `xray api ado`. Adapted from SB Gateway 1.6.21 xray_outbound.go and
// xray_inbound.go. Manager.Switch adds a versioned tag before sending it.
func RenderOutbound(node catalog.Node) (json.RawMessage, error) {
	source, err := renderSourceOutbound(node)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(source, &value); err != nil {
		return nil, err
	}
	var outbound map[string]any
	switch node.Protocol {
	case "vless":
		settings := map[string]any{
			"address": node.Server, "port": node.Port,
			"id": node.Credentials["uuid"], "encryption": "none",
		}
		if flow, _ := value["flow"].(string); flow != "" {
			settings["flow"] = flow
		}
		stream, err := renderVLESSStream(value)
		if err != nil {
			return nil, err
		}
		outbound = map[string]any{"protocol": "vless", "settings": settings, "streamSettings": stream}
	case "vmess", "trojan", "shadowsocks":
		settings := map[string]any{"address": node.Server, "port": node.Port}
		if node.Protocol == "vmess" {
			settings["id"] = node.Credentials["uuid"]
			settings["security"] = value["security"]
		} else {
			settings["password"] = node.Credentials["password"]
		}
		if node.Protocol == "shadowsocks" {
			settings["method"] = value["method"]
		}
		stream, err := renderVLESSStream(value)
		if err != nil {
			return nil, err
		}
		outbound = map[string]any{"protocol": node.Protocol, "settings": settings, "streamSettings": stream}
	case "hysteria2":
		stream := map[string]any{
			"network": "hysteria", "security": "tls",
			"sockopt":          map[string]any{"domainStrategy": "UseIPv4"},
			"hysteriaSettings": map[string]any{"version": 2, "auth": node.Credentials["password"], "udpIdleTimeout": 60},
		}
		if tls := object(value["tls"]); len(tls) > 0 {
			settings, err := renderClientTLS(tls)
			if err != nil {
				return nil, err
			}
			stream["tlsSettings"] = settings
		}
		if obfs := object(value["obfs"]); obfs["type"] == "salamander" {
			stream["finalmask"] = map[string]any{"udp": []any{map[string]any{
				"type": "salamander", "settings": map[string]any{"password": node.Credentials["obfs_password"]},
			}}}
		}
		outbound = map[string]any{
			"protocol": "hysteria", "settings": map[string]any{"version": 2, "address": node.Server, "port": node.Port},
			"streamSettings": stream,
		}
	default:
		return nil, errors.New("unsupported Xray protocol")
	}
	if node.Protocol == "shadowsocks" {
		if plugin, ok := node.Settings["plugin"]; ok {
			outbound["_resproxy_plugin"] = plugin
		}
	}
	return json.Marshal(outbound)
}

func renderVLESSStream(value map[string]any) (map[string]any, error) {
	stream := map[string]any{"network": "tcp", "sockopt": map[string]any{"domainStrategy": "UseIPv4"}}
	transport := object(value["transport"])
	kind, _ := transport["type"].(string)
	switch kind {
	case "", "tcp":
		if transport["header_type"] == "http" {
			request := map[string]any{"path": []string{fallbackText(transport["path"], "/")}}
			if host := transportHost(transport); host != "" {
				request["headers"] = map[string]any{"Host": strings.Split(host, ",")}
			}
			stream["tcpSettings"] = map[string]any{"header": map[string]any{"type": "http", "request": request}}
		}
	case "ws":
		settings := map[string]any{"path": fallbackText(transport["path"], "/")}
		if host := transportHost(transport); host != "" {
			settings["host"] = host
		}
		if headers := transportHeaders(transport); len(headers) > 0 {
			settings["headers"] = headers
		}
		stream["network"], stream["wsSettings"] = "ws", settings
	case "grpc":
		stream["network"], stream["grpcSettings"] = "grpc", map[string]any{"serviceName": fallbackText(transport["service_name"], "")}
	case "httpupgrade":
		settings := map[string]any{"path": fallbackText(transport["path"], "/")}
		if host := transportHost(transport); host != "" {
			settings["host"] = host
		}
		if headers := transportHeaders(transport); len(headers) > 0 {
			settings["headers"] = headers
		}
		stream["network"], stream["httpupgradeSettings"] = "httpupgrade", settings
	case "xhttp":
		stream["network"] = "xhttp"
		stream["xhttpSettings"] = map[string]any{
			"mode": fallbackText(transport["mode"], "auto"), "host": fallbackText(transport["host"], ""),
			"path":  fallbackText(transport["path"], "/"),
			"extra": map[string]any{"xPaddingBytes": fallbackText(transport["x_padding_bytes"], "100-1000")},
		}
	default:
		return nil, fmt.Errorf("unsupported Xray transport %q", kind)
	}
	tls := object(value["tls"])
	if tls["enabled"] != true {
		return stream, nil
	}
	if reality := object(tls["reality"]); reality["enabled"] == true {
		fingerprint := fallbackText(object(tls["utls"])["fingerprint"], "chrome")
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]any{
			"serverName": fallbackText(tls["server_name"], ""), "fingerprint": fingerprint,
			"password": fallbackText(reality["public_key"], ""), "shortId": fallbackText(reality["short_id"], ""),
		}
		return stream, nil
	}
	settings, err := renderClientTLS(tls)
	if err != nil {
		return nil, err
	}
	stream["security"], stream["tlsSettings"] = "tls", settings
	return stream, nil
}

func renderClientTLS(tls map[string]any) (map[string]any, error) {
	if tls["insecure"] == true {
		return nil, errors.New("Xray TLS cannot disable certificate verification")
	}
	settings := map[string]any{"serverName": fallbackText(tls["server_name"], "")}
	for source, target := range map[string]string{
		"pinned_peer_cert_sha256":  "pinnedPeerCertSha256",
		"verify_peer_cert_by_name": "verifyPeerCertByName",
	} {
		if text := fallbackText(tls[source], ""); text != "" {
			settings[target] = text
		}
	}
	if fingerprint := fallbackText(object(tls["utls"])["fingerprint"], ""); fingerprint != "" {
		settings["fingerprint"] = fingerprint
	}
	if alpn, ok := tls["alpn"].([]any); ok {
		settings["alpn"] = alpn
	}
	return settings, nil
}

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func fallbackText(value any, fallback string) string {
	text, _ := value.(string)
	if text == "" {
		return fallback
	}
	return text
}

func transportHost(transport map[string]any) string {
	if host := fallbackText(transport["host"], ""); host != "" {
		return host
	}
	for key, value := range object(transport["headers"]) {
		if strings.EqualFold(key, "host") {
			return fallbackText(value, "")
		}
	}
	return ""
}

func transportHeaders(transport map[string]any) map[string]any {
	headers := make(map[string]any)
	for key, value := range object(transport["headers"]) {
		if !strings.EqualFold(key, "host") {
			headers[key] = value
		}
	}
	return headers
}
