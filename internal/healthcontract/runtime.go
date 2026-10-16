package healthcontract

import "time"

// RuntimeSnapshot is deliberately independent of retained measurement history.
// Its publication time is a lease, not evidence that a selector was confirmed.
type RuntimeSnapshot struct {
	UpdatedAt time.Time                `json:"updated_at"`
	Policies  map[string]RuntimePolicy `json:"policies"`
}

type RuntimePolicy struct {
	Selected          string `json:"selected"`
	RuntimeSelected   string `json:"runtime_selected"`
	RuntimeConfirmed  bool   `json:"runtime_confirmed"`
	WANFallbackActive bool   `json:"wan_fallback_active"`
	RuntimeError      string `json:"runtime_error,omitempty"`
	LastSwitchReason  string `json:"last_switch_reason,omitempty"`
	CheckedAt         string `json:"checked_at,omitempty"`
}
