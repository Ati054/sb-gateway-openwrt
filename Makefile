# OpenWrt 25.12 source package. Install this repository as a local package
# feed entry or symlink it into package/resproxy in the matching SDK.
include $(TOPDIR)/rules.mk

PKG_NAME:=sb-gateway-openwrt
PKG_VERSION:=0.1.0
PKG_RELEASE:=84
PKG_LICENSE:=AGPL-3.0-only AND MPL-2.0 AND MIT AND Apache-2.0 AND BSD-3-Clause
PKG_LICENSE_FILES:=LICENSE XRAY-LICENSE DOMAIN-LIST-LICENSE
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk

define Package/sb-gateway-openwrt
  SECTION:=net
  CATEGORY:=Network
  TITLE:=SB Gateway for OpenWrt
  DEPENDS:=+ca-bundle +firewall4 +nftables +ip-full +kmod-nft-tproxy +kmod-nft-socket +luci-base
endef

define Package/sb-gateway-openwrt/description
  Independent OpenWrt client proxy using one Xray core, live subscription
  generations, and the SB Gateway best/priority health policy controller.
endef

define Package/sb-gateway-openwrt/conffiles
/etc/config/resproxy
/etc/resproxy/firewall.nft
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) $(CURDIR)/go.mod $(CURDIR)/go.sum $(PKG_BUILD_DIR)/
	$(CP) $(CURDIR)/cmd $(CURDIR)/internal $(CURDIR)/vendor $(CURDIR)/third_party $(PKG_BUILD_DIR)/
	$(CP) $(CURDIR)/LICENSE $(PKG_BUILD_DIR)/
	$(CP) $(CURDIR)/files/domain-list-community.LICENSE $(PKG_BUILD_DIR)/DOMAIN-LIST-LICENSE
	$(CP) $(CURDIR)/files/xray.LICENSE $(PKG_BUILD_DIR)/XRAY-LICENSE
endef

# This package is built for the user's mediatek/filogic ARM64 target. The
# vendored dependencies allow offline builds without CGO or module downloads.
# A Linux Go 1.27.2 host toolchain is required by the SDK build host.
define Build/Compile
	cd $(PKG_BUILD_DIR) && GOTOOLCHAIN=go1.27.2 GOMAXPROCS=2 \
		GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -p=2 -mod=vendor -trimpath -gcflags=all=-l=4 -buildvcs=false -ldflags='-s -w -X main.version=$(PKG_VERSION)-r$(PKG_RELEASE) -buildid= -X github.com/xtls/xray-core/core.build=b26a91de4f3294e26a0ad0a970b81a386a41f789' \
		-o $(PKG_BUILD_DIR)/resproxyd ./cmd/resproxyd
endef

define Package/sb-gateway-openwrt/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/resproxyd $(1)/usr/bin/resproxyd
	$(INSTALL_DIR) $(1)/usr/libexec/resproxy
	ln -s ../../bin/resproxyd $(1)/usr/libexec/resproxy/xray
	$(INSTALL_DIR) $(1)/usr/share/licenses/resproxy-xray
	$(INSTALL_DATA) ./files/xray.LICENSE $(1)/usr/share/licenses/resproxy-xray/LICENSE
	$(INSTALL_DIR) $(1)/usr/share/licenses/resproxy-domain-list-community
	$(INSTALL_DATA) ./files/domain-list-community.LICENSE $(1)/usr/share/licenses/resproxy-domain-list-community/LICENSE
	$(INSTALL_DIR) $(1)/usr/share/licenses/resproxy-go
	$(INSTALL_DATA) ./files/go-dependency-licenses/* $(1)/usr/share/licenses/resproxy-go/
	$(INSTALL_DIR) $(1)/etc/config $(1)/etc/init.d $(1)/etc/resproxy
	$(INSTALL_DIR) $(1)/usr/libexec
	$(INSTALL_CONF) ./files/resproxy.config $(1)/etc/config/resproxy
	$(INSTALL_BIN) ./files/resproxy.init $(1)/etc/init.d/resproxy
	$(INSTALL_BIN) ./files/resproxy-firewall $(1)/usr/libexec/resproxy-firewall
	$(INSTALL_DIR) $(1)/etc/hotplug.d/iface
	$(INSTALL_BIN) ./files/resproxy-hotplug $(1)/etc/hotplug.d/iface/95-resproxy
	$(INSTALL_CONF) ./files/firewall.nft $(1)/etc/resproxy/firewall.nft
	$(INSTALL_DIR) $(1)/usr/share/nftables.d/ruleset-post
	$(INSTALL_DATA) ./files/resproxy-fw4.nft $(1)/usr/share/nftables.d/ruleset-post/50-resproxy.nft
	$(INSTALL_BIN) ./files/resproxy-status $(1)/usr/libexec/resproxy-status
	$(INSTALL_BIN) ./files/resproxy-health $(1)/usr/libexec/resproxy-health
	$(INSTALL_BIN) ./files/resproxy-locations $(1)/usr/libexec/resproxy-locations
	$(INSTALL_BIN) ./files/resproxy-plugins $(1)/usr/libexec/resproxy-plugins
	$(INSTALL_BIN) ./files/resproxy-refresh $(1)/usr/libexec/resproxy-refresh
	$(INSTALL_BIN) ./files/resproxy-log $(1)/usr/libexec/resproxy-log
	$(INSTALL_BIN) ./files/resproxy-rulesets $(1)/usr/libexec/resproxy-rulesets
	$(INSTALL_BIN) ./files/resproxy-rulesets-refresh $(1)/usr/libexec/resproxy-rulesets-refresh
	$(INSTALL_BIN) ./files/sb-gateway-upgrade $(1)/usr/libexec/sb-gateway-upgrade
	$(INSTALL_DIR) $(1)/usr/share/luci/menu.d $(1)/usr/share/rpcd/acl.d
	$(INSTALL_DATA) ./files/luci-app-resproxy.json $(1)/usr/share/luci/menu.d/luci-app-resproxy.json
	$(INSTALL_DATA) ./files/luci-app-resproxy-acl.json $(1)/usr/share/rpcd/acl.d/luci-app-resproxy.json
	$(INSTALL_DIR) $(1)/www/luci-static/resources/view/resproxy
	$(INSTALL_DATA) ./htdocs/luci-static/resources/view/resproxy/*.js $(1)/www/luci-static/resources/view/resproxy/
	$(INSTALL_DATA) ./htdocs/luci-static/resources/view/resproxy/*.css $(1)/www/luci-static/resources/view/resproxy/
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/i18n
	$(INSTALL_DATA) ./files/resproxy.ru.lmo $(1)/usr/lib/lua/luci/i18n/resproxy.ru.lmo
endef

define Package/sb-gateway-openwrt/postinst
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] && exit 0
# Remove LuCI's hashed page-tree cache when package menu entries change.
rm -f /tmp/luci-indexcache /tmp/luci-indexcache.*.json
# An r11 guarded update leaves its supervisor running. Wait for that one
# transition to activate before releasing the old on-flash recovery cache.
if [ -d /etc/sb-gateway-upgrade ]; then
	/usr/libexec/sb-gateway-upgrade migrate-legacy >/dev/null 2>&1 </dev/null &
fi
exit 0
endef

$(eval $(call BuildPackage,sb-gateway-openwrt))
