package agentport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"iter"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
)

func (pool healthPool) hasOutbound(id string) bool {
	return pool.Outbounds[id] != nil || pool.CatalogKeys[id] != ""
}

func (pool healthPool) outboundIDs() iter.Seq[string] {
	return func(yield func(string) bool) {
		for id := range pool.Outbounds {
			if !yield(id) {
				return
			}
		}
		for id := range pool.CatalogKeys {
			if pool.Outbounds[id] == nil && !yield(id) {
				return
			}
		}
	}
}

func (pool healthPool) outboundIdentity(id string) string {
	if body := pool.Outbounds[id]; body != nil {
		return string(body)
	}
	return "sha256:" + pool.CatalogKeys[id]
}

func (pool healthPool) outboundDefinition(ctx context.Context, id string) (json.RawMessage, error) {
	if body := pool.Outbounds[id]; body != nil {
		return body, nil
	}
	expected := pool.CatalogKeys[id]
	if expected == "" || pool.CatalogRef == nil {
		return nil, errors.New("node is absent from the health pool")
	}
	body, err := catalog.StoredDefinition(ctx, &catalog.RecordLocator{Root: pool.CatalogRef, ID: id})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != expected {
		return nil, errors.New("outbound definition does not match its health contract")
	}
	return body, nil
}
