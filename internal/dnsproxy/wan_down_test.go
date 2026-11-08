package dnsproxy

import (
	"context"
	"testing"
)

func TestWANConfirmedDownDiscardsOldResolver(t *testing.T) {
	available := true
	state := &wanDNSState{lookup: func(context.Context) ([]Endpoint, error) {
		if !available {
			return nil, errWANDNSUnavailable
		}
		return []Endpoint{{Address: "192.0.2.53:53", Interface: "eth1"}}, nil
	}}
	state.refresh(t.Context())
	available = false
	if err := state.refresh(t.Context()); err != nil || len(state.current.Load().servers) != 0 {
		t.Fatal("confirmed down kept a stale resolver")
	}
}
