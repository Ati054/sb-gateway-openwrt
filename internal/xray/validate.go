package xray

import (
	"encoding/json"
	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/xtls/xray-core/infra/conf"
)

// ValidateOutbound compiles connection settings without opening sockets or
// replacing handlers. Caller reports counts, not credential-bearing errors.
func ValidateOutbound(node catalog.Node) error {
	body, err := RenderOutbound(node)
	if err != nil {
		return err
	}
	var outbound conf.OutboundDetourConfig
	if err := json.Unmarshal(body, &outbound); err != nil {
		return err
	}
	_, err = outbound.Build()
	return err
}
