package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

// renderSourceOutbound builds the intermediate client schema used by SB Gateway
// 1.6.21. render_legacy.go converts it to the actual Xray API wire schema.
func renderSourceOutbound(node catalog.Node) (json.RawMessage, error) {
	if node.Server == "" || node.Port < 1 || node.Port > 65535 {
		return nil, errors.New("invalid outbound endpoint")
	}
	outbound := map[string]any{
		"type": node.Protocol, "server": node.Server, "server_port": node.Port,
	}
	switch node.Protocol {
	case "vless", "vmess":
		if node.Credentials["uuid"] == "" {
			return nil, errors.New("UUID credentials are missing")
		}
		outbound["uuid"] = node.Credentials["uuid"]
		if flow, err := optionalString(node.Settings, "flow"); err != nil {
			return nil, err
		} else if flow != "" {
			outbound["flow"] = flow
		}
		for _, field := range []string{"tls", "transport"} {
			if value, err := optionalObject(node.Settings, field); err != nil {
				return nil, err
			} else if len(value) != 0 {
				outbound[field] = value
			}
		}
	case "trojan", "shadowsocks":
		if node.Credentials["password"] == "" {
			return nil, errors.New("Password credentials are missing")
		}
		outbound["password"] = node.Credentials["password"]
		for _, field := range []string{"tls", "transport"} {
			if value, err := optionalObject(node.Settings, field); err != nil {
				return nil, err
			} else if len(value) != 0 {
				outbound[field] = value
			}
		}
		if node.Protocol == "shadowsocks" {
			method, err := optionalString(node.Settings, "method")
			if err != nil || method == "" {
				return nil, errors.New("Shadowsocks method is missing")
			}
			outbound["method"] = method
		}
	case "hysteria2":
		if node.Credentials["password"] == "" {
			return nil, errors.New("Hysteria 2 credentials are missing")
		}
		outbound["password"] = node.Credentials["password"]
		for _, field := range []string{"up_mbps", "down_mbps"} {
			if value, exists := node.Settings[field]; exists {
				number, ok := nonnegativeInteger(value)
				if !ok {
					return nil, fmt.Errorf("invalid %s setting", field)
				}
				outbound[field] = number
			}
		}
		if tls, err := optionalObject(node.Settings, "tls"); err != nil {
			return nil, err
		} else if len(tls) != 0 {
			outbound["tls"] = tls
		}
		if obfs, err := optionalObject(node.Settings, "obfs"); err != nil {
			return nil, err
		} else if len(obfs) != 0 {
			if obfs["type"] != "salamander" || node.Credentials["obfs_password"] == "" {
				return nil, errors.New("invalid Hysteria 2 obfuscation settings")
			}
			outbound["obfs"] = map[string]any{"type": "salamander", "password": node.Credentials["obfs_password"]}
		}
	default:
		return nil, errors.New("unsupported outbound protocol")
	}
	if node.Protocol == "vmess" {
		security, err := optionalString(node.Settings, "security")
		if err != nil {
			return nil, err
		}
		if security == "" {
			security = "auto"
		}
		outbound["security"] = security
	}
	return json.Marshal(outbound)
}

func nonnegativeInteger(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, number >= 0
	case float64: // encoding/json decodes map values to float64.
		if number >= 0 && number <= math.MaxInt && math.Trunc(number) == number {
			return int(number), true
		}
	}
	return 0, false
}

func optionalString(settings map[string]any, key string) (string, error) {
	value, exists := settings[key]
	if !exists {
		return "", nil
	}
	text, ok := value.(string)
	if !ok || strings.ContainsAny(text, "\r\n") {
		return "", fmt.Errorf("invalid %s setting", key)
	}
	return text, nil
}

func optionalObject(settings map[string]any, key string) (map[string]any, error) {
	value, exists := settings[key]
	if !exists {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid %s setting", key)
	}
	copy := make(map[string]any, len(object))
	for field, item := range object {
		copy[field] = item
	}
	return copy, nil
}
