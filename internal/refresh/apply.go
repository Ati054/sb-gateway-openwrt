// Package refresh coordinates a subscription generation with a live Xray
// selector. Fetching, health-based choice and old-handler retirement are
// separate responsibilities so they cannot force an Xray/procd restart here.
package refresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/Ati054/sb-gateway-openwrt/internal/catalog"
	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
	"github.com/Ati054/sb-gateway-openwrt/internal/subscription"
	"github.com/Ati054/sb-gateway-openwrt/internal/xray"
)

type Runtime interface {
	Switch(context.Context, string, string, json.RawMessage) (xray.SwitchResult, error)
	Restore(context.Context, string, string) error
}

type Choose func([]catalog.Node) (string, error)

type Service struct {
	Store       generation.Store
	Runtime     Runtime
	AfterSwitch func(previous, next []catalog.Node) (rollback func() error, err error)
	mu          sync.Mutex
}

type Result struct {
	GenerationID string
	SelectedID   string
	OldTag       string // Retire later, only after a drain period.
	Delta        catalog.Delta
	ParseStats   subscription.ParseStats
}

// Apply keeps the current generation untouched until parse, reconcile,
// rendering, staging and live selector readback all succeed. The choice is
// supplied by the health controller and must refer to the new full catalog.
// An empty choice imports an unselected catalog without switching traffic.
func (s *Service) Apply(ctx context.Context, providerID string, feed []byte, maxNodes int, selector string, choose Choose) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Runtime == nil || choose == nil || selector == "" {
		return Result{}, errors.New("refresh runtime, selector and choice are required")
	}
	previous := []catalog.Node{}
	previousID, current, err := s.Store.Current()
	if err == nil {
		previous = current
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("current generation cannot be verified: %w", err)
	}
	parsed, stats, err := subscription.ParseValidated(providerID, feed, maxNodes, xray.ValidateOutbound)
	if err != nil {
		return Result{}, err
	}
	oldProvider := make([]catalog.Node, 0)
	for _, node := range previous {
		if node.ProviderID == providerID {
			oldProvider = append(oldProvider, node)
		}
	}
	updated, delta, err := catalog.Reconcile(providerID, oldProvider, parsed)
	if err != nil {
		return Result{}, err
	}
	next := make([]catalog.Node, 0, len(previous)-len(oldProvider)+len(updated))
	for _, node := range previous {
		if node.ProviderID != providerID {
			next = append(next, node)
		}
	}
	next = append(next, updated...)
	selectedID, err := choose(next)
	if err != nil {
		return Result{}, err
	}
	var definition json.RawMessage
	if selectedID != "" {
		var selected *catalog.Node
		for index := range next {
			if next[index].ID == selectedID {
				selected = &next[index]
				break
			}
		}
		if selected == nil {
			return Result{}, errors.New("health choice is absent from the new catalog")
		}
		definition, err = xray.RenderOutbound(*selected)
		if err != nil {
			return Result{}, err
		}
	}
	metadataOnly := s.Store.MetadataRoot != "" && catalog.InformationalChangesOnly(previous, next)
	id := previousID
	if !metadataOnly {
		id, err = s.Store.Stage(next)
		if err != nil {
			return Result{}, err
		}
	}

	var switched xray.SwitchResult
	if selectedID != "" {
		switched, err = s.Runtime.Switch(ctx, selector, selectedID, definition)
		if err != nil {
			return Result{}, err
		}
	}
	var rollbackRuntime func() error
	if s.AfterSwitch != nil {
		rollbackRuntime, err = s.AfterSwitch(previous, next)
		if err != nil {
			if switched.Changed && switched.PreviousTag == "" {
				return Result{}, fmt.Errorf("runtime pool update failed and prior selector is unknown: %w", err)
			}
			if switched.Changed && switched.PreviousTag != "" {
				if restoreErr := s.Runtime.Restore(ctx, selector, switched.PreviousTag); restoreErr != nil {
					return Result{}, fmt.Errorf("runtime pool update failed and selector restore is uncertain: %w; restore: %v", err, restoreErr)
				}
			}
			return Result{}, fmt.Errorf("runtime pool update failed: %w", err)
		}
	}
	var publishErr error
	if metadataOnly {
		publishErr = s.Store.PublishMetadata(id, next)
	} else {
		publishErr = s.Store.Publish(id)
	}
	if err := publishErr; err != nil {
		if rollbackRuntime != nil {
			if rollbackErr := rollbackRuntime(); rollbackErr != nil {
				return Result{}, fmt.Errorf("generation publication failed and runtime pool rollback is uncertain: %w; rollback: %v", err, rollbackErr)
			}
		}
		if switched.Changed && switched.PreviousTag == "" {
			return Result{}, fmt.Errorf("generation publication failed and prior selector is unknown: %w", err)
		}
		if switched.Changed {
			if restoreErr := s.Runtime.Restore(ctx, selector, switched.PreviousTag); restoreErr != nil {
				return Result{}, fmt.Errorf("publish failed and selector restore is uncertain: %w; restore: %v", err, restoreErr)
			}
		}
		return Result{}, fmt.Errorf("generation publication failed: %w", err)
	}
	oldTag := ""
	if switched.Changed {
		oldTag = switched.PreviousTag
	}
	return Result{GenerationID: id, SelectedID: selectedID, OldTag: oldTag, Delta: delta, ParseStats: stats}, nil
}
