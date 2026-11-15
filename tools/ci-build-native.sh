#!/bin/sh
set -eu
target=${1:-/tmp/xroute-ci}
version=$(sed -n 's/^PKG_VERSION:=\([0-9][0-9.]*\)$/\1/p' Makefile)
pinned=$(sed -n 's/.*core.build=\([a-f0-9]\{40\}\).*/\1/p' Makefile)
[ -n "$version" ] && [ "${#pinned}" -eq 40 ]
mkdir -p "$target"
CGO_ENABLED=0 go build -p=1 -mod=vendor -trimpath -gcflags=all=-l=4 -buildvcs=false \
 -ldflags="-s -w -buildid= -X main.version=$version -X github.com/Ati054/xroute-openwrt/internal/daemon.urlTestPlanningDefault=route-paired -X github.com/xtls/xray-core/core.build=$pinned -X github.com/xtls/xray-core/common/session.dnsOriginDefault=1" \
 -o "$target/resproxyd" ./cmd/resproxyd
ln -sf resproxyd "$target/xray"
if [ -n "${GITHUB_ENV:-}" ]; then
 printf 'RESPROXY_TEST_XRAY=%s/xray\nRESPROXY_RECOVERY_BINARY=%s/xray\n' "$target" "$target" >> "$GITHUB_ENV"
fi
