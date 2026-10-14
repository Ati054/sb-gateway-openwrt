#!/bin/sh
set -eu
[ "$#" -eq 1 ] || exit 2
case "$1" in
 generic) sdk=/root/sdk/openwrt-sdk-25.12.5-armsr-armv8_gcc-14.3.0_musl.Linux-x86_64; arch=aarch64_generic;;
 filogic) sdk=/root/sdk/openwrt-sdk-25.12.5-mediatek-filogic_gcc-14.3.0_musl.Linux-x86_64; arch=aarch64_cortex-a53;;
 *) exit 2;;
esac
# Serialize SDK source replacement and package database writes.
exec 8>"$sdk/package/.resproxy-build.lock"
flock -x 8
source=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
[ -f "$sdk/rules.mk" ];[ -f "$source/vendor/modules.txt" ];[ -f "$source/third_party/xray-core/core/xray.go" ]
version=$(sed -n 's/^PKG_VERSION:=\([0-9][A-Za-z0-9._+~-]*\)$/\1/p' "$source/Makefile")
[ -n "$version" ]
release=$(sed -n 's/^PKG_RELEASE:=\([0-9][0-9]*\)$/\1/p' "$source/Makefile")
if [ -n "$release" ]; then version=$version-r$release; fi
# A complete source stage prevents retired Go files surviving an SDK overlay.
stage=$(mktemp -d "$sdk/package/.resproxy-stage-XXXXXX")
previous=
cleanup_stage() {
 case "$stage" in
  "$sdk/package/".resproxy-stage-*)
   [ -L "$stage" ] || rm -rf -- "$stage";;
 esac
}
trap cleanup_stage EXIT
cd "$source"
tar -cf - Makefile go.mod go.sum cmd internal LICENSE files htdocs vendor third_party | tar -xf - -C "$stage"
if [ -d "$sdk/package/resproxy" ];then
 previous=$(mktemp -d "$sdk/package/.resproxy-previous-XXXXXX")
 mv "$sdk/package/resproxy" "$previous/source"
fi
mv "$stage" "$sdk/package/resproxy"
make -C "$sdk/package/resproxy" TOPDIR="$sdk" clean V=s
make -C "$sdk/package/resproxy" TOPDIR="$sdk" compile V=s
mkdir -p "$source/dist"
cp "$sdk/bin/packages/$arch/base/sb-gateway-openwrt-$version.apk" "$source/dist/sb-gateway-openwrt-$version-$arch.apk"
# Release keys stay outside the source stage and are provided by the maintainer.
if [ -n "${SB_GATEWAY_SIGN_KEY:-}" ]; then
 "$sdk/staging_dir/host/bin/apk" --allow-untrusted adbsign --reset-signatures --sign-key "$SB_GATEWAY_SIGN_KEY" "$source/dist/sb-gateway-openwrt-$version-$arch.apk"
 "$sdk/staging_dir/host/bin/apk" --keys-dir "$source/files" verify "$source/dist/sb-gateway-openwrt-$version-$arch.apk"
fi
# Keep the previous sources on failure for recovery, but do not accumulate a
# complete vendor/source copy after every successful build and delivery.
case "$previous" in
 "$sdk/package/".resproxy-previous-*)
  [ -L "$previous" ] || rm -rf -- "$previous";;
esac
