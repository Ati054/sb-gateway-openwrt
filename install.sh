#!/bin/sh
# Install the latest published SB Gateway package on OpenWrt.
set -eu
umask 077
repo=https://github.com/Ati054/sb-gateway-openwrt
[ "$(id -u)" = 0 ] || { echo 'Run this command as root.' >&2; exit 1; }
command -v apk >/dev/null || { echo 'OpenWrt with APK is required.' >&2; exit 1; }
command -v jsonfilter >/dev/null || { echo 'Install jsonfilter: apk add jsonfilter' >&2; exit 1; }
arch=$(cat /etc/apk/arch 2>/dev/null || :)
case "$arch" in aarch64_generic|aarch64_cortex-a53) ;; *) echo "No APK for architecture $arch." >&2; exit 1;;esac
work=$(mktemp -d /tmp/sb-gateway-install.XXXXXX)
bootstrap_key_added=0
fetch_pid=
project_key=/etc/apk/keys/ati054-sb-gateway.rsa.pub
cleanup() {
 if [ -n "$fetch_pid" ]; then kill "$fetch_pid" 2>/dev/null || :; wait "$fetch_pid" 2>/dev/null || :; fi
 rm -rf "$work"
 if [ "$bootstrap_key_added" = 1 ] && ! apk info -e sb-gateway-openwrt >/dev/null 2>&1; then rm -f "$project_key"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
fetch() {
 limit=${3:-1048576}
 fifo=$work/download.pipe
 mkfifo "$fifo"
 if command -v uclient-fetch >/dev/null; then uclient-fetch -q -T 30 -O - "$1" > "$fifo" &
 elif command -v wget >/dev/null; then wget -q -T 30 -O - "$1" > "$fifo" &
 else rm -f "$fifo"; echo 'wget or uclient-fetch is required.' >&2; return 1; fi
 fetch_pid=$!
 if ! head -c "$((limit+1))" < "$fifo" > "$2"; then
  kill "$fetch_pid" 2>/dev/null || :; wait "$fetch_pid" 2>/dev/null || :
  fetch_pid=; rm -f "$fifo"; return 1
 fi
 download_status=0
 wait "$fetch_pid" || download_status=$?
 fetch_pid=; rm -f "$fifo"
 [ "$(wc -c < "$2")" -le "$limit" ] || { echo 'Download exceeds the size limit.' >&2; return 1; }
 [ "$download_status" = 0 ] && [ -s "$2" ] || { echo 'Download failed.' >&2; return 1; }
}

echo 'Checking release...'
fetch "https://api.github.com/repos/Ati054/sb-gateway-openwrt/releases?per_page=20" "$work/releases.json"
[ "$(wc -c < "$work/releases.json")" -le 1048576 ] || { echo 'Invalid release list.' >&2; exit 1; }
tag=$(jsonfilter -i "$work/releases.json" -e '@[@.draft=false && @.prerelease=false].tag_name' | head -n 1)
printf '%s' "$tag" | grep -Eq '^v[0-9][A-Za-z0-9._-]{0,79}$' || { echo 'No published release yet.' >&2; exit 1; }
base=$repo/releases/download/$tag
fetch "$base/release.json" "$work/release.json" 16384
[ "$(wc -c < "$work/release.json")" -le 16384 ] || { echo 'Invalid release metadata.' >&2; exit 1; }
[ "$(jsonfilter -i "$work/release.json" -e '@.schema')" = 1 ] || exit 1
[ "$(jsonfilter -i "$work/release.json" -e '@.tag')" = "$tag" ] || exit 1
# New installed controllers authenticate metadata with their pinned key. First
# installation and migration from legacy unsigned versions bootstrap over HTTPS.
fetch "$base/release.json.sig" "$work/release.json.sig" 256
if apk info -e sb-gateway-openwrt >/dev/null 2>&1; then
 if resproxyd update-key >/dev/null 2>&1; then
  resproxyd verify-release "$work/release.json" "$work/release.json.sig"
 else
  current=$(resproxyd version)
  [ "$(apk version -t "$current" 0.1.1)" != '>' ] || { echo 'Release verification is unavailable.' >&2; exit 1; }
 fi
fi
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
fetch "$base/$name" "$work/package.apk" "$size"
[ "$(wc -c < "$work/package.apk")" = "$size" ] || { echo 'APK download is incomplete.' >&2; exit 1; }
printf '%s  %s\n' "$sha" "$work/package.apk" | sha256sum -c -
mkdir "$work/keys"
cat > "$work/keys/sb-gateway.rsa.pub" <<'SB_GATEWAY_PUBLIC_KEY'
-----BEGIN PUBLIC KEY-----
MIIBojANBgkqhkiG9w0BAQEFAAOCAY8AMIIBigKCAYEA13G+qoMeBOVx32Vz5lSN
vsOHH3cGYOFymfYL3te0xUrl6CW+KfDXRefgBCnDBvbtHM/oaNt3TfI7ZUH6KyKW
J9WpsOdl/2/+3hxL0QS8dhvA24T6lTkx/gnws3kbg3UNS8pAtx1+BsuO0xGAEhQE
JxSu5h1ocLQDorzLOV87J2w4RAJ2DeM5POQMQiJEgzYaxbDmEUiXFAVizm5pC5zZ
cL4zIzBegI+1+4HV/tVj+9tF2seNCcE4LsjM8T73Op4ZhyUB4l0kgxcqWWK3kp0l
zx+304WDS+HD8sSr119CYHcXmB6DGoZiPg/MmJopdF1gYlkWbTX+QHXiwkK6cv2J
EUTpB/PjahUvkGN89HNlTf9wCwSQIzyNeg75/d6ewhrP2CwTyqNaOKZXjK0FIG5R
uYyTNE6w+Nm6WQEkAEazdlI/ZZHr9GZPZCsfBeSQYBGMIOQZXA+j/ukYlu3M7jXA
TCwKA7JFV1tR2+GuqvYDuxJOkp9p3H46sBL4b7iNX2IHAgMBAAE=
-----END PUBLIC KEY-----
SB_GATEWAY_PUBLIC_KEY
apk --keys-dir "$work/keys" verify "$work/package.apk" >/dev/null || exit 1
if apk info -e sb-gateway-openwrt >/dev/null 2>&1; then
 [ -x /usr/libexec/sb-gateway-upgrade ] || { echo 'Update the installed package through LuCI.' >&2; exit 1; }
 mv "$work/package.apk" /tmp/sb-gateway-upgrade.apk
 /usr/libexec/sb-gateway-upgrade start "$sha"
 echo 'Update started. Open SB Gateway > Update in LuCI for its status.'
else
 project_key=/etc/apk/keys/ati054-sb-gateway.rsa.pub
 mkdir -p /etc/apk/keys
 if [ -e "$project_key" ]; then
  cmp "$project_key" "$work/keys/sb-gateway.rsa.pub" >/dev/null || { echo 'Installed signing key differs.' >&2; exit 1; }
 else
  cp "$work/keys/sb-gateway.rsa.pub" "$project_key"
  bootstrap_key_added=1
  chmod 644 "$project_key"
 fi
 installed_size=$(apk adbdump "$work/package.apk" | awk '$1=="installed-size:" {print $2; exit}')
 printf '%s' "$installed_size" | grep -Eq '^[0-9]+$' || exit 1
 overlay=$(df -Pk /overlay | awk 'NR==2 {print $4}')
 [ "$overlay" -ge "$(( (installed_size+1023)/1024+4096 ))" ] || { echo 'Not enough free flash for installation.' >&2; exit 1; }
 apk --no-cache add --simulate "$work/package.apk"
 apk --no-cache add "$work/package.apk"
 echo 'Installed. Open Services > SB Gateway in LuCI.'
fi
