package sip003

import (
	"errors"
	"sort"
	"strings"
)

type Config struct {
	Name    string `json:"name"`
	Options string `json:"options"`
}

// A share link can select these two known executables; it cannot supply an
// executable path, server mode, certificate files or arbitrary process args.
func Parse(value string) (Config, error) {
	fail := func() (Config, error) { return Config{}, errors.New("unsupported Shadowsocks plugin or options") }
	if len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") {
		return fail()
	}
	parts, ok := splitOptions(value)
	if !ok || len(parts) == 0 {
		return fail()
	}
	name := parts[0]
	if name == "simple-obfs" {
		name = "obfs-local"
	}
	if name != "v2ray-plugin" && name != "obfs-local" {
		return fail()
	}
	options := map[string]string{}
	for _, part := range parts[1:] {
		key, v, has := strings.Cut(part, "=")
		if key == "" {
			continue
		}
		if _, exists := options[key]; exists {
			return fail()
		}
		allowed := false
		if name == "v2ray-plugin" {
			switch key {
			case "tls":
				allowed = !has
			case "mode":
				allowed = has && (v == "websocket" || v == "quic")
			case "host":
				allowed = has && safeHost(v)
			case "path":
				allowed = has && strings.HasPrefix(v, "/") && !strings.ContainsAny(v, "\x00\r\n")
			case "mux":
				allowed = has && (v == "0" || v == "1" || v == "2" || v == "4" || v == "8")
			}
		} else {
			switch key {
			case "obfs":
				allowed = has && (v == "http" || v == "tls")
			case "obfs-host":
				allowed = has && safeHost(v)
			case "obfs-uri":
				allowed = has && strings.HasPrefix(v, "/") && !strings.ContainsAny(v, "\x00\r\n")
			}
		}
		if !allowed {
			return fail()
		}
		options[key] = v
	}
	if name == "obfs-local" && options["obfs"] == "" {
		return fail()
	}
	keys := []string{}
	for k := range options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	normalized := []string{}
	for _, k := range keys {
		part := k
		if k != "tls" {
			part += "=" + escape(options[k])
		}
		normalized = append(normalized, part)
	}
	return Config{name, strings.Join(normalized, ";")}, nil
}
func safeHost(v string) bool { return v != "" && !strings.ContainsAny(v, " /\\\t;=\r\n") }
func escape(v string) string {
	return strings.NewReplacer("\\", "\\\\", ";", "\\;", "=", "\\=").Replace(v)
}
func splitOptions(v string) ([]string, bool) {
	parts := []string{}
	var b strings.Builder
	escaped := false
	for _, r := range v {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == ';' {
			parts = append(parts, b.String())
			b.Reset()
		} else {
			b.WriteRune(r)
		}
	}
	if escaped {
		return nil, false
	}
	parts = append(parts, b.String())
	return parts, true
}
func Binary(name string) string {
	if name == "v2ray-plugin" {
		return "/usr/bin/v2ray-plugin"
	}
	if name == "obfs-local" {
		return "/usr/bin/obfs-local"
	}
	return ""
}
