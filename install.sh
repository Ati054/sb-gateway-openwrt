#!/bin/sh
# Install or migrate XRoute using a project-signed APK.
set -eu
umask 077
repo=https://github.com/Ati054/xroute-openwrt
[ "$(id -u)" = 0 ] || { echo 'Run this command as root.' >&2; exit 1; }
command -v apk >/dev/null || { echo 'OpenWrt with APK is required.' >&2; exit 1; }
command -v jsonfilter >/dev/null || { echo 'Install jsonfilter: apk add jsonfilter' >&2; exit 1; }
local_apk=
case "$#:${1:-}" in
 0:) ;;
 2:--local) local_apk=$2; [ -f "$local_apk" ] && [ ! -L "$local_apk" ] || exit 1;;
 *) echo 'Usage: install.sh [--local /tmp/xroute.apk]' >&2; exit 2;;
esac
arch=$(cat /etc/apk/arch 2>/dev/null || :)
case "$arch" in aarch64_generic|aarch64_cortex-a53|x86_64) ;; *) echo "No APK for architecture $arch." >&2; exit 1;;esac
work=$(mktemp -d /tmp/xroute-install.XXXXXX)
bootstrap_key_added=0
fetch_pid=
setup_pid=
setup_watchdog_pid=
project_key=/etc/apk/keys/ati054-sb-gateway.rsa.pub
is_installed() { apk info -e xroute-openwrt >/dev/null 2>&1 || apk info -e sb-gateway-openwrt >/dev/null 2>&1; }
cleanup() {
 stop_setup
 if [ -n "$fetch_pid" ]; then kill "$fetch_pid" 2>/dev/null || :; wait "$fetch_pid" 2>/dev/null || :; fi
 rm -rf "$work"
 if [ "$bootstrap_key_added" = 1 ] && ! is_installed; then rm -f "$project_key"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
check_ram() {
 ram=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
 free=$(df -Pk /tmp | awk 'NR==2 {print $4}')
 printf '%s:%s' "$ram" "$free" | grep -Eq '^[0-9]+:[0-9]+$' || exit 1
 [ "$ram" -ge "$1" ] && [ "$free" -ge "$1" ] || { echo 'Not enough free RAM for installation.' >&2; exit 1; }
}
stop_setup_watchdog() {
 if [ -n "$setup_watchdog_pid" ]; then
  kill "$setup_watchdog_pid" 2>/dev/null || :; wait "$setup_watchdog_pid" 2>/dev/null || :
  setup_watchdog_pid=
 fi
}
stop_setup() {
 stop_setup_watchdog
 if [ -n "$setup_pid" ]; then
  kill -KILL "-$setup_pid" 2>/dev/null || :
  kill -KILL "$setup_pid" 2>/dev/null || :
  wait "$setup_pid" 2>/dev/null || :
  setup_pid=
 fi
}
run_bounded() {
 seconds=$1; shift
 command -v setsid >/dev/null || { echo 'Install curl with apk add curl, then run the installer again.' >&2; return 1; }
 setsid "$@" &
 setup_pid=$!
 (
  timer_pid=
  trap 'if [ -n "$timer_pid" ]; then kill "$timer_pid" 2>/dev/null || :; wait "$timer_pid" 2>/dev/null || :; fi; exit 0' TERM INT HUP
  sleep "$seconds" & timer_pid=$!; wait "$timer_pid" || exit 0
  kill -TERM "-$setup_pid" 2>/dev/null || exit 0
  sleep 2 & timer_pid=$!; wait "$timer_pid" || exit 0
  kill -KILL "-$setup_pid" 2>/dev/null || :
 ) &
 setup_watchdog_pid=$!
 result=0; wait "$setup_pid" || result=$?
 stop_setup
 return "$result"
}
fetch() {
 limit=${3:-1048576}
 curl_setup_attempted=0
 while :; do
  fifo=$work/download.pipe
  download_log=$work/download.log
  mkfifo "$fifo"
  if command -v curl >/dev/null; then
   downloader=curl
   curl -fLsS --connect-timeout 15 --max-time 180 --max-redirs 5 --proto '=https' --proto-redir '=https' -o - "$1" > "$fifo" 2> "$download_log" &
  elif command -v uclient-fetch >/dev/null; then
   downloader=uclient-fetch
   uclient-fetch -T 30 -O - "$1" > "$fifo" 2> "$download_log" &
  elif command -v wget >/dev/null; then
   downloader=wget
   wget -T 30 -O - "$1" > "$fifo" 2> "$download_log" &
  else
   rm -f "$fifo"; echo 'curl, wget or uclient-fetch is required.' >&2; return 1
  fi
  fetch_pid=$!
  if ! head -c "$((limit+1))" < "$fifo" > "$2"; then
   kill "$fetch_pid" 2>/dev/null || :; wait "$fetch_pid" 2>/dev/null || :
   fetch_pid=; rm -f "$fifo" "$2"; return 1
  fi
  if [ "$(wc -c < "$2")" -gt "$limit" ]; then
   kill "$fetch_pid" 2>/dev/null || :; wait "$fetch_pid" 2>/dev/null || :
   fetch_pid=; rm -f "$fifo" "$2"
   echo 'Download exceeds the size limit.' >&2; return 1
  fi
  download_status=0
  wait "$fetch_pid" || download_status=$?
  fetch_pid=; rm -f "$fifo"
  if [ "$download_status" = 0 ] && [ -s "$2" ]; then return 0; fi
  rm -f "$2"
  if [ "$downloader" != curl ] && [ "$curl_setup_attempted" = 0 ] && ! command -v curl >/dev/null; then
   curl_setup_attempted=1
   echo 'Installing curl from OpenWrt repositories for HTTPS downloads...' >&2
   check_ram 8192
   if run_bounded 120 apk update > "$work/curl-setup.log" 2>&1 && run_bounded 120 apk add --no-progress curl >> "$work/curl-setup.log" 2>&1 && command -v curl >/dev/null; then
    continue
   fi
   echo 'Could not install curl from the configured OpenWrt repositories.' >&2
   cat "$work/curl-setup.log" >&2; return 1
  fi
  echo "Download failed: $1 (exit $download_status)." >&2
  cat "$download_log" >&2; return 1
 done
}

if [ -n "$local_apk" ]; then
 size=$(wc -c < "$local_apk")
 [ "$size" -gt 0 ] && [ "$size" -le 67108864 ] || exit 1
 check_ram "$(( (size+1023)/1024 + 32768 ))"
 cp "$local_apk" "$work/package.apk"
 sha=$(sha256sum "$work/package.apk" | cut -d ' ' -f 1)
else
 echo 'Checking release...'
 fetch "$repo/releases/latest/download/release.json" "$work/release.json" 16384
 [ "$(jsonfilter -i "$work/release.json" -e '@.schema')" = 1 ] || exit 1
 tag=$(jsonfilter -i "$work/release.json" -e '@.tag')
 printf '%s' "$tag" | grep -Eq '^v[0-9][A-Za-z0-9._-]{0,79}$' || exit 1
 base=$repo/releases/download/$tag
 fetch "$base/release.json.sig" "$work/release.json.sig" 256
 if is_installed; then
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
 [ "$name" = "xroute-openwrt-$version-$arch.apk" ] || exit 1
 printf '%s' "$size" | grep -Eq '^[0-9]{1,9}$' || exit 1
 [ "$size" -gt 0 ] && [ "$size" -le 67108864 ] || exit 1
 printf '%s' "$sha" | grep -Eq '^[0-9a-f]{64}$' || exit 1
 if is_installed; then
  current=$(resproxyd version)
  if [ "$(apk version -t "$version" "$current")" != '>' ]; then
   echo "XRoute $current is already installed."; exit 0
  fi
 fi
 check_ram "$(( (size+1023)/1024 + 32768 ))"
 echo "Downloading XRoute $version ($arch)..."
 fetch "$base/$name" "$work/package.apk" "$size"
 [ "$(wc -c < "$work/package.apk")" = "$size" ] || exit 1
 printf '%s  %s\n' "$sha" "$work/package.apk" | sha256sum -c -
fi
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
metadata=$(apk adbdump "$work/package.apk")
field() { printf '%s\n' "$metadata" | awk -v key="$1" '$1==key":" {print $2; exit}'; }
[ "$(field name)" = xroute-openwrt ] || { echo 'This APK is not XRoute.' >&2; exit 1; }
[ "$(field arch)" = "$arch" ] || { echo 'APK architecture differs from this router.' >&2; exit 1; }
installed_size=$(field installed-size)
printf '%s' "$installed_size" | grep -Eq '^[0-9]+$' || exit 1
if is_installed; then
 # Old LuCI accepts only the old APK identity. Bootstrap the new updater
 # exclusively from the APK whose signature we just checked, not the network.
 # Delete the extracted payload before the worker's compact-RAM preflight.
 check_ram "$(( (installed_size+1023)/1024 + 32768 ))"
 mkdir "$work/payload"
 apk --keys-dir "$work/keys" extract --destination "$work/payload" "$work/package.apk" >/dev/null
 [ -f "$work/payload/usr/libexec/sb-gateway-upgrade" ] && [ ! -L "$work/payload/usr/libexec/sb-gateway-upgrade" ] || exit 1
 cp "$work/payload/usr/libexec/sb-gateway-upgrade" "$work/upgrade"
 chmod 700 "$work/upgrade"
 rm -rf "$work/payload"
 [ ! -e /var/run/sb-gateway-upgrade/busy ] || { echo 'Another update is in progress.' >&2; exit 1; }
 mv "$work/package.apk" /tmp/sb-gateway-upgrade.apk
 /bin/sh "$work/upgrade" start "$sha"
 echo 'Update started. Open XRoute > Update in LuCI for its status.'
else
 mkdir -p /etc/apk/keys
 if [ -e "$project_key" ]; then
  cmp "$project_key" "$work/keys/sb-gateway.rsa.pub" >/dev/null || { echo 'Installed signing key differs.' >&2; exit 1; }
 else
  cp "$work/keys/sb-gateway.rsa.pub" "$project_key"
  bootstrap_key_added=1
  chmod 644 "$project_key"
 fi
 overlay=$(df -Pk /overlay | awk 'NR==2 {print $4}')
 printf '%s' "$overlay" | grep -Eq '^[0-9]+$' || exit 1
 [ "$overlay" -ge "$(( (installed_size+1023)/1024+4096 ))" ] || { echo 'Not enough free flash for installation.' >&2; exit 1; }
 apk --no-cache add --simulate "$work/package.apk"
 apk --no-cache add "$work/package.apk"
 echo 'Installed. Open Services > XRoute in LuCI.'
fi
