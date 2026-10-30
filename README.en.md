# SB Gateway for OpenWrt

[Русский](README.md) | English

Route devices and services through proxies with automatic node selection. Configure and monitor everything in LuCI; Xray is included. The interface supports English, Russian, Simplified and Traditional Chinese, and Persian.

**Subscription updates, node switching, and monitoring interval changes take effect without restarting the service.**

## Features

- **Subscriptions:** multiple sources; links, Clash YAML, sing-box JSON, and Xray JSON. Invalid and unsupported nodes are skipped; manual and scheduled updates. Failed updates preserve the working catalog; successful updates take effect without restarting the core.
- **Protocols:** VLESS, VMess, Trojan, Shadowsocks, and Hysteria2. Shadowsocks SIP003 plugins v2ray-plugin and obfs-local can be installed from the interface.
- **Route lists:** separate settings for all devices, selected IP addresses, or subnets. Pick devices from OpenWrt static DHCP reservations or enter addresses manually. Select nodes by name, subscription, country, location, protocol, and filters.
- **Name filters:** comma-separated or `|`-separated fragments are alternatives. Double quotes require a full name: `Sweden, "Amsterdam Premium 01"` matches names containing Sweden and the specific Amsterdam node. Exclusion uses the same syntax and takes precedence.
- **URLTest:** compares full HTTPS response times using a short median of up to three fresh measurements. Switching requires two independent confirmations of the configured improvement; active and candidate measurements must complete within 10 seconds of each other. Failure checks run separately.
- **Priority / reserve:** uses nodes in the specified order, falls back when a node fails, and returns to an available node with higher priority.
- **Presets:** country rules for Russia, China, and Iran; service cards for YouTube, Discord, Telegram, WhatsApp, social networks, AI services, games, exchanges, and smart homes. Each exchange card includes its website and API.
- **Custom rules:** domains, IP networks, ports, TCP/UDP, and detected protocols. Send traffic through the route list's proxy or directly through WAN; the first matching rule determines the route.
- **All proxies unavailable:** an independent rule-database setting blocks proxy traffic or temporarily sends it through WAN. Available for preset variants and custom rules; selected cards keep their separate routing action. Traffic returns to the proxy when a node recovers.
- **System protocols:** local addresses, VPN routes, and direct IP exceptions take precedence over WAN restrictions. Multicast, broadcast, and related ICMP errors are preserved. WAN ping can be blocked; GRE/ESP are blocked outside exceptions. IPv6 direct mode does not restrict other protocols.
- **DNS:** proxy and WAN resolvers, DoH/DoT, and device queries handled according to their route list and rules. DNS WAN fallback follows the rule’s proxy failure setting. An active external DNS service keeps OpenWrt DNS; handing over https-dns-proxy requires separate confirmation.
- **Monitoring:** active nodes and backups, availability, HTTPS errors, latency, and the last valid paired Delta. Hover over Delta to see both measurement times; negative means faster and positive means slower. Data refreshes without reloading the page; the active node appears first, with up to 10 nodes in the table. Process memory, available RAM, and active TCP/UDP sessions are shown.
- **Updates:** check GitHub and install with a button while preserving settings; manual APK upload is also available. Packages are verified against the project signing key. Compact updates keep the APK and backup in RAM.

The controller and Xray run as two processes from one executable. Measurement history and logs stay in RAM with size limits. Applying settings preserves history and the selected node if it remains eligible for the route list. If no working proxy node is available, the behavior selected in the rule database applies: blocking or WAN fallback. Direct WAN rules remain active. DNS follows the same rule when the proxy fails. Strict route lists stay protected during router boot, service restarts and package updates. Direct IP exceptions and VPN routes remain available, including for DNS. Explicitly stopping the service removes interception.

Keep the router powered during an update. When flash space is low, compact installation keeps the recovery copy in RAM; a power interruption at that point may require reinstalling the APK. Configuration is retained. To recover, transfer the APK using the instructions below and run `apk --no-cache add --force-reinstall /tmp/sb-gateway.apk`.

## Installation

**OpenWrt 25.12 with APK, ARM64 and x86_64.** The package architecture is listed in `/etc/apk/arch`:

At least 256 MB RAM is recommended. On routers with 128 MB flash, available space depends on the firmware; the installer checks it before installation.

| Architecture | APK |
|---|---|
| `aarch64_generic` | ARM64 Generic build |
| `aarch64_cortex-a53` | Cortex-A53 / Filogic build |
| `x86_64` | x86-64 build |

Run this in a terminal on your PC, including Windows PowerShell. Replace `192.168.1.1` with your router's IP address:

```sh
ssh root@192.168.1.1 "wget -O /tmp/sb-gateway-install.sh https://raw.githubusercontent.com/Ati054/sb-gateway-openwrt/main/install.sh && sh /tmp/sb-gateway-install.sh"
```

The installer detects the architecture, checks available space, downloads the latest stable version, verifies the APK signature and SHA-256, and installs dependencies.

### If the router cannot access GitHub

1. Check the router's architecture. Run this in PowerShell, replacing the IP address:

   ```powershell
   ssh root@192.168.1.1 "cat /etc/apk/arch"
   ```

2. Download the APK with the matching architecture from [Releases](https://github.com/Ati054/sb-gateway-openwrt/releases). For example, `aarch64_cortex-a53` needs the file ending in `-aarch64_cortex-a53.apk`. Save it on your PC as `sb-gateway.apk`. Download the [project signing key](https://raw.githubusercontent.com/Ati054/sb-gateway-openwrt/main/files/update-key.pub) into the same folder as `ati054-sb-gateway.rsa.pub`.
3. Open the folder in File Explorer, right-click an empty area, and select **Open in Terminal**. Select PowerShell. On Windows 10: **Shift + right-click → Open PowerShell window here**.
4. Transfer the APK and key, add the key, and install the package:

   ```powershell
   scp -O .\sb-gateway.apk .\ati054-sb-gateway.rsa.pub root@192.168.1.1:/tmp/
   ssh root@192.168.1.1 "mkdir -p /etc/apk/keys && cp /tmp/ati054-sb-gateway.rsa.pub /etc/apk/keys/ati054-sb-gateway.rsa.pub && apk --no-cache add /tmp/sb-gateway.apk"
   ```

`-O` uses SCP with Dropbear. The APK signature is checked with the project key; dependencies are installed from OpenWrt repositories.

The project key is installed in `/etc/apk/keys`: APK trusts any package signed by that key.

### Setup

The service is disabled after the first installation. Open **Services → SB Gateway for OpenWrt** in LuCI:

1. Add subscriptions and download nodes while the service is still disabled.
2. Create rules and route lists, then select devices and nodes.
3. Configure DNS, enable SB Gateway, save, and apply settings.

When switching from PassWall2, stop it and disable its autostart in **System → Startup** before enabling SB Gateway. Subscriptions and rules can be prepared while SB Gateway is disabled. Without an all-device list, other addresses continue to use WAN.

During service startup and recovery, temporary WAN access with the OpenWrt DNS resolver is available only to lists that permit WAN for all proxy traffic. Strict lists and their DNS remain blocked. If settings cannot be read or validated, proxy traffic remains blocked. Domain-based rules require a working core; service recovery continues automatically.

To update, open **Package update → Check for updates → Update from GitHub**. Checking only fetches version information; download and installation start after a separate click on **Update from GitHub**. You can skip intermediate versions. Settings and the service's enabled or disabled state are preserved; a running service restarts during the update.

## Building

Use the OpenWrt 25.12 SDK for your ARM64 or x86/64 target and Go 1.27.3. Place the repository in `SDK/package/resproxy` and run:

```sh
make -C SDK/package/resproxy TOPDIR=/absolute/path/to/SDK compile V=s
```

Dependencies are included in `vendor`; the core source is in `third_party/xray-core`.

## Licenses

The project uses AGPL-3.0. Core and dependency licenses are preserved in [LICENSE](LICENSE), [NOTICE.md](NOTICE.md), `vendor`, and `third_party`.
