package subscription

import (
	"bytes"
	"encoding/json"

	"example.org/resilient-proxy/internal/catalog"
)

func canonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func cloneJSONObject(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneJSONValue(item)
	}
	return result
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONObject(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneJSONValue(item)
		}
		return result
	default:
		return value
	}
}

func subscriptionText(value any) string {
	text, _ := value.(string)
	return text
}

func subscriptionNodeProtocol(node map[string]any) string {
	protocol := subscriptionText(node["protocol"])
	if protocol == "" {
		protocol = "vless"
	}
	tls, _ := node["tls"].(map[string]any)
	reality, _ := tls["reality"].(map[string]any)
	if protocol == "vless" && reality != nil && reality["enabled"] != false {
		return "reality"
	}
	return protocol
}

func subscriptionTransport(node map[string]any) string {
	transport, _ := node["transport"].(map[string]any)
	kind := subscriptionText(transport["type"])
	if kind == "" {
		return "tcp"
	}
	return kind
}

// Parse uses the SB Gateway link parser and converts its normalized nodes into
// the OpenWrt client catalog. Credentials remain in memory for the later
// dedicated secret store; callers must never serialize them into public files.
func Parse(providerID string, body []byte, maxNodes int) ([]catalog.Node, error) {
	normalized, err := parseProxySubscription(body, maxNodes)
	if err != nil {
		return nil, err
	}
	result := make([]catalog.Node, 0, len(normalized))
	for _, raw := range normalized {
		settings := make(map[string]any)
		for _, key := range []string{"flow", "tls", "transport", "up_mbps", "down_mbps", "obfs", "method", "security", "plugin"} {
			if value, exists := raw[key]; exists {
				settings[key] = cloneJSONValue(value)
			}
		}
		credentials := make(map[string]string)
		for _, pair := range [][2]string{{"_uuid", "uuid"}, {"_password", "password"}, {"_obfs_password", "obfs_password"}} {
			if value := subscriptionText(raw[pair[0]]); value != "" {
				credentials[pair[1]] = value
			}
		}
		port, _ := raw["server_port"].(int)
		result = append(result, catalog.Node{
			ProviderID: providerID, Label: subscriptionText(raw["label"]),
			Country: subscriptionText(raw["country"]), City: subscriptionText(raw["city"]),
			Protocol: subscriptionText(raw["protocol"]), Transport: subscriptionTransport(raw),
			Server: subscriptionText(raw["server"]), Port: port, Settings: settings, Credentials: credentials,
		})
	}
	return result, nil
}
