package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/Ati054/sb-gateway-openwrt/internal/capture"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

// libuci is available before rpcd and netifd. Keep the snapshot in memory and
// never print subscription credentials or reparse shell-escaped UCI values.
func ReadProtectionSystemUCI(ctx context.Context) (Config, error) {
	const script = `import { cursor } from 'uci'; let c=cursor(); let result={}; for (let name in ['resproxy','network']) { if (!c.load(name)) die('Cannot load protection configuration'); let sections={}; let index=0; c.foreach(name,null,function(s) { s['.index']=index++; sections[s['.name']]=s; }); result[name]=sections; } print(result);`
	body, err := exec.CommandContext(ctx, "/usr/bin/ucode", "-e", script).Output()
	if err != nil {
		return Config{}, fmt.Errorf("read early UCI: %w", err)
	}
	var packages map[string]map[string]map[string]any
	if err = json.Unmarshal(body, &packages); err != nil {
		return Config{}, err
	}
	get, err := snapshotGetter(packages)
	if err != nil {
		return Config{}, err
	}
	return protectionConfig(ctx, get)
}

func protectionConfig(ctx context.Context, get Getter) (Config, error) {
	config, err := ReadUCI(ctx, get)
	if err == nil {
		return config, nil
	}
	if ctx.Err() != nil {
		return Config{}, ctx.Err()
	}
	// An unrelated invalid setting must never authorize WAN or disable startup
	// protection. Use a minimal closed policy and only a validated LAN device.
	read := func(key string) string { v, _ := get(ctx, key); return strings.TrimSpace(v) }
	config = Config{Enabled: read("resproxy.main.enabled") == "1", RouteEnabled: read("resproxy.main.route_enabled") == "1", Base: xray.DefaultBaseConfig()}
	if !config.Enabled || !config.RouteEnabled {
		return config, nil
	}
	config.LANInterface = read("network.lan.device")
	if !regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`).MatchString(config.LANInterface) {
		return Config{}, errors.New("invalid protection LAN device")
	}
	config.Capture, err = capture.New(read("resproxy.main.ipv6_mode"), strings.Fields(read("resproxy.main.bypass_cidrs")))
	if err != nil {
		config.Capture, _ = capture.New("proxy", nil)
	}
	config.Capture.BlockPing = read("resproxy.main.block_ping") == "1"
	return config, nil
}
