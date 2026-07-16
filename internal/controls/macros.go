package controls

// The Launchkey-style macro slots. There are 8 global slots, each
// optionally assigned to a board param. This layer is deliberately thin:
// it STORES the assignments (globally, in state.toml) and BROADCASTS them
// as a "macros" change so every surface stays in sync. It does NOT apply
// anything to the engine — the web UI drives each assigned Target param
// through the existing setters (SetChainParam, SetVolume, …). Target is an
// opaque string here; the web validates it against the board params.

import (
	"errors"
	"math"

	"github.com/mschulkind-oss/polyclav/internal/state"
)

// ErrInvalidMacro is returned by SetMacros for a malformed assignment
// array: a slot outside 1..8, a duplicate slot, or a non-finite min/max.
// The web layer maps it to 400.
var ErrInvalidMacro = errors.New("invalid macro assignment")

// Macros returns the stored (assigned) macro slots (a clone; see
// state.Store.Macros). Read-only, no applyMu — mirrors PedalOrder().
func (c *Controls) Macros() []state.Macro {
	return c.st.Macros()
}

// SetMacros validates and replaces the global 8-slot macro assignments,
// persists them, and publishes a "macros" change. Each slot must be in
// 1..8 with no duplicates and finite min/max; Target is opaque (the web
// validates it against the board params). Mirrors SetPedalOrder's
// lock/validate/persist/publish discipline and is likewise allowed with no
// patch selected (the assignments are global). Rejects an invalid array
// with ErrInvalidMacro, leaving the stored assignments untouched.
func (c *Controls) SetMacros(m []state.Macro) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	seen := make(map[int]bool, len(m))
	for _, mac := range m {
		if mac.Slot < 1 || mac.Slot > 8 {
			return ErrInvalidMacro
		}
		if seen[mac.Slot] {
			return ErrInvalidMacro
		}
		seen[mac.Slot] = true
		if !macroFinite(mac.Min) || !macroFinite(mac.Max) {
			return ErrInvalidMacro
		}
	}
	if err := c.st.SetMacros(m); err != nil {
		return err
	}
	c.hub.Publish(Change{Type: "macros", Data: map[string]any{
		"macros": append([]state.Macro(nil), m...), // clone — don't alias the caller's slice
	}})
	return nil
}

// macroFinite reports whether v is neither NaN nor ±Inf — the same
// non-finite rejection the web layer applies to wire floats.
func macroFinite(v float32) bool {
	return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0)
}
