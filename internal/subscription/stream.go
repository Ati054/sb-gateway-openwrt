package subscription

import (
	"context"
	"errors"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

// StreamValidated emits one accepted connection at a time. Zero removes the
// count cap; byte, structured-record and YAML limits remain independently active.
// A storage or cancellation error aborts the transaction, never truncates it.
func StreamValidated(ctx context.Context, providerID string, body []byte, maxNodes int, validate func(catalog.Node) error, emit func(catalog.Node) error) (ParseStats, error) {
	if emit == nil || providerID == "" {
		return ParseStats{}, errors.New("subscription destination and source are required")
	}
	var check func(map[string]any) error
	if validate != nil {
		check = func(raw map[string]any) error { return validate(catalogNode(providerID, raw)) }
	}
	_, stats, err := parseProxySubscriptionTo(body, maxNodes, check, func(raw map[string]any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return emit(catalogNode(providerID, raw))
	})
	return stats, err
}
