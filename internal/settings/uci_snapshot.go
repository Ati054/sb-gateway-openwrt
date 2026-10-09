package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// ReadSystemUCI uses one rpcd snapshot per package instead of spawning uci for
// every option. Large saved rule sets must not exhaust the startup deadline or
// mix values from different edits. LuCI supplies rpcd's UCI service.
func ReadSystemUCI(ctx context.Context) (Config, error) {
	snapshots := map[string]map[string]map[string]any{}
	for _, name := range []string{"resproxy", "network"} {
		request, _ := json.Marshal(map[string]string{"config": name})
		body, err := exec.CommandContext(ctx, "/bin/ubus", "call", "uci", "get", string(request)).Output()
		if err != nil {
			return Config{}, fmt.Errorf("read UCI package %s: %w", name, err)
		}
		var reply struct {
			Values map[string]map[string]any `json:"values"`
		}
		if err := json.Unmarshal(body, &reply); err != nil {
			return Config{}, fmt.Errorf("decode UCI package %s: %w", name, err)
		}
		if len(reply.Values) == 0 {
			return Config{}, fmt.Errorf("empty UCI package %s", name)
		}
		snapshots[name] = reply.Values
	}
	get, err := snapshotGetter(snapshots)
	if err != nil {
		return Config{}, err
	}
	return ReadUCI(ctx, get)
}

func snapshotGetter(packages map[string]map[string]map[string]any) (Getter, error) {
	flat := map[string]string{}
	for name, sections := range packages {
		ordered := make([]string, 0, len(sections))
		for id, section := range sections {
			if _, ok := section[".index"].(float64); !ok {
				return nil, fmt.Errorf("UCI package %s lacks section order", name)
			}
			ordered = append(ordered, id)
		}
		sort.Slice(ordered, func(i, j int) bool {
			return sections[ordered[i]][".index"].(float64) < sections[ordered[j]][".index"].(float64)
		})
		counts := map[string]int{}
		for _, id := range ordered {
			section := sections[id]
			typ, ok := section[".type"].(string)
			if !ok || typ == "" {
				return nil, fmt.Errorf("UCI package %s lacks section type", name)
			}
			indexed := fmt.Sprintf("%s.@%s[%d]", name, typ, counts[typ])
			counts[typ]++
			for key, raw := range section {
				if strings.HasPrefix(key, ".") {
					continue
				}
				var value string
				switch raw := raw.(type) {
				case string:
					value = raw
				case []any:
					list := make([]string, len(raw))
					for i, item := range raw {
						text, ok := item.(string)
						if !ok {
							return nil, fmt.Errorf("UCI package %s has invalid list", name)
						}
						list[i] = text
					}
					value = strings.Join(list, " ")
				default:
					return nil, fmt.Errorf("UCI package %s has invalid option", name)
				}
				flat[name+"."+id+"."+key] = value
				flat[indexed+"."+key] = value
			}
		}
	}
	return func(ctx context.Context, key string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		value, ok := flat[key]
		if !ok {
			return "", errors.New("missing UCI option")
		}
		return value, nil
	}, nil
}
