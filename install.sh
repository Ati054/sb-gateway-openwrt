#!/bin/sh
# Install the published SB Gateway Release Candidate on OpenWrt.
set -eu
umask 077
repo=https://github.com/Ati054/sb-gateway-openwrt
[ "$(id -u)" = 0 ] || { echo 'Run this command as root.' >&2; exit 1; }
command -v apk >/dev/null || { echo 'OpenWrt with APK is required.' >&2; exit 1; }
command -v jsonfilter >/dev/null || { echo 'Install jsonfilter: apk add jsonfilter' >&2; exit 1; }
arch=$(cat /etc/apk/arch 2>/dev/null || :)
case "$arch" in aarch64_generic|aarch64_cortex-a53) ;; *) echo "No APK for architecture $arch." >&2; exit 1;;esac
work=$(mktemp -d /tmp/sb-gateway-install.XXXXXX)
trap 'rm -rf "$work"' EXIT INT TERM
fetch() {
 if command -v uclient-fetch >/dev/null; then uclient-fetch -q -O "$2" "$1"
 elif command -v wget >/dev/null; then wget -q -O "$2" "$1"
 else echo 'wget or uclient-fetch is required.' >&2; return 1; fi
}
echo 'Checking release...'
fetch "https://api.github.com/repos/Ati054/sb-gateway-openwrt/releases?per_page=20" "$work/releases.json"
[ "$(wc -c < "$work/releases.json")" -le 1048576 ] || { echo 'Invalid release list.' >&2; exit 1; }
tag=$(jsonfilter -i "$work/releases.json" -e '@[*].tag_name' | head -n 1)
printf '%s' "$tag" | grep -Eq '^v[0-9][A-Za-z0-9._-]{0,79}$' || { echo 'No published release yet.' >&2; exit 1; }
base=$repo/releases/download/$tag
fetch "$base/release.json" "$work/release.json"
[ "$(wc -c < "$work/release.json")" -le 16384 ] || { echo 'Invalid release metadata.' >&2; exit 1; }
[ "$(jsonfilter -i "$work/release.json" -e '@.schema')" = 1 ] || exit 1
[ "$(jsonfilter -i "$work/release.json" -e '@.tag')" = "$tag" ] || exit 1
version=$(jsonfilter -i "$work/release.json" -e '@.version')
printf '%s' "$version" | grep -Eq '^[0-9][A-Za-z0-9._+~-]{0,79}$' || exit 1
entry=$(jsonfilter -i "$work/release.json" -e "@.assets[@.arch=\"$arch\"]")
name=$(jsonfilter -s "$entry" -e '@.name')
size=$(jsonfilter -s "$entry" -e '@.size')
sha=$(jsonfilter -s "$entry" -e '@.sha256')
[ "$name" = "sb-gateway-openwrt-$version-$arch.apk" ] || exit 1
printf '%s' "$size" | grep -Eq '^[0-9]{1,9}$' || exit 1
[ "$size" -gt 0 ] && [ "$size" -le 67108864 ] || exit 1
printf '%s' "$sha" | grep -Eq '^[0-9a-f]{64}$' || exit 1
if apk info -e sb-gateway-openwrt >/dev/null 2>&1; then
 current=$(resproxyd version)
 if [ "$(apk version -t "$version" "$current")" != '>' ]; then
  echo "SB Gateway $current is already installed."; exit 0
 fi
fi
ram=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
free=$(df -Pk /tmp | awk 'NR==2 {print $4}')
needed=$(( (size + 1023) / 1024 + 32768 ))
[ "$ram" -ge "$needed" ] && [ "$free" -ge "$needed" ] || { echo 'Not enough free RAM for installation.' >&2; exit 1; }
echo "Downloading SB Gateway $version ($arch)..."
fetch "$base/$name" "$work/package.apk"
[ "$(wc -c < "$work/package.apk")" = "$size" ] || { echo 'APK download is incomplete.' >&2; exit 1; }
printf '%s  %s\n' "$sha" "$work/package.apk" | sha256sum -c -
apk verify --allow-untrusted "$work/package.apk" >/dev/null || exit 1
if apk info -e sb-gateway-openwrt >/dev/null 2>&1; then
 [ -x /usr/libexec/sb-gateway-upgrade ] || { echo 'Update the installed package through LuCI.' >&2; exit 1; }
 mv "$work/package.apk" /tmp/sb-gateway-upgrade.apk
 /usr/libexec/sb-gateway-upgrade start "$sha"
 echo 'Update started. Open SB Gateway > Update in LuCI for its status.'
else
 installed_size=$(apk adbdump "$work/package.apk" | awk '$1=="installed-size:" {print $2; exit}')
 printf '%s' "$installed_size" | grep -Eq '^[0-9]+$' || exit 1
 overlay=$(df -Pk /overlay | awk 'NR==2 {print $4}')
 [ "$overlay" -ge "$(( (installed_size+1023)/1024+4096 ))" ] || { echo 'Not enough free flash for installation.' >&2; exit 1; }
 apk --no-cache add --simulate --allow-untrusted "$work/package.apk"
 apk --no-cache add --allow-untrusted "$work/package.apk"
 echo 'Installed. Open Services > SB Gateway in LuCI.'
fi
