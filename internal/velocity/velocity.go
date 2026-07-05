// Package velocity remaps note velocity through a gamma (power) curve
// with an output clamp, per docs/VELOCITY_CURVES.md.
//
// The remap lives here in Go — at the funnel point just before
// audio.PushMIDI — so a single curve covers every synth backend
// uniformly and the real-time audio thread is never touched. The
// package is deliberately pure and config-free: it must not import
// internal/config, so config wiring (FromConfig-style plumbing) lives
// with the caller and this package stays trivially testable.
package velocity

import (
	"fmt"
	"math"
)

// Preset gamma values. Named presets exist so nobody has to think in
// exponents, and so the numbers can be retuned by ear later without
// breaking configs (see the design doc's Open Questions).
const (
	gammaSoft   float32 = 0.6 // lifts the middle: heavy keybeds / quiet patches
	gammaLinear float32 = 1.0 // identity
	gammaHard   float32 = 1.6 // suppresses the middle: light keybeds / shouty patches
)

// Curve is an immutable velocity remap: out(v) = clamp(round(127·(v/127)^γ),
// outMin, outMax), with v==0 passed through untouched. Value semantics
// (small, copyable) so callers can swap it atomically on patch select.
type Curve struct {
	gamma  float32
	outMin uint8
	outMax uint8
}

// New builds a curve from a preset name or a custom gamma.
//
// curve is "" or "linear" (γ=1), "soft" (γ=0.6), "hard" (γ=1.6), or
// "custom" (gamma must be > 0; it is ignored for named presets so a
// stale gamma left in config can't silently distort a preset).
// outMin==0 defaults to 1 and outMax==0 defaults to 127, letting
// callers pass zero values for "no clamp configured".
//
// Errors: unknown curve name; curve=="custom" with gamma<=0 (or NaN);
// outMin>outMax after defaulting.
func New(curve string, gamma float32, outMin, outMax uint8) (Curve, error) {
	g := gammaLinear
	switch curve {
	case "", "linear":
		g = gammaLinear
	case "soft":
		g = gammaSoft
	case "hard":
		g = gammaHard
	case "custom":
		// !(gamma > 0) rather than gamma <= 0 so NaN is rejected too.
		if !(gamma > 0) {
			return Curve{}, fmt.Errorf("velocity: curve %q requires gamma > 0, got %v", curve, gamma)
		}
		g = gamma
	default:
		return Curve{}, fmt.Errorf("velocity: unknown curve %q (want \"soft\", \"linear\", \"hard\", or \"custom\")", curve)
	}

	if outMin == 0 {
		outMin = 1 // out_min ≥ 1: a played note must never remap to NoteOff
	}
	if outMax == 0 {
		outMax = 127
	}
	if outMin > outMax {
		return Curve{}, fmt.Errorf("velocity: out_min %d > out_max %d", outMin, outMax)
	}
	return Curve{gamma: g, outMin: outMin, outMax: outMax}, nil
}

// Linear returns the identity curve (γ=1, out 1..127). It is the
// explicit default; a zero-value Curve is NOT a usable curve (its clamp
// range is 0..0), so callers must start from Linear() or New().
func Linear() Curve {
	return Curve{gamma: gammaLinear, outMin: 1, outMax: 127}
}

// Apply remaps one velocity byte. It is total and monotonic
// non-decreasing over 0..127 for any γ>0 (the power function is
// monotonic, and rounding and clamping preserve that).
//
// v==0 always returns 0: NoteOn with velocity 0 is NoteOff semantics on
// the wire and must be preserved exactly. For v>=1 the result is
// clamped to [outMin, outMax] and never falls below 1, so a played note
// can never be turned into a NoteOff by the curve.
func (c Curve) Apply(v uint8) uint8 {
	if v == 0 {
		return 0
	}
	if v > 127 {
		v = 127 // MIDI velocity is 7-bit; treat out-of-range defensively
	}
	out := math.Round(127 * math.Pow(float64(v)/127, float64(c.gamma)))
	if out < float64(c.outMin) {
		out = float64(c.outMin)
	}
	if out > float64(c.outMax) {
		out = float64(c.outMax)
	}
	if out < 1 {
		out = 1 // invariant even for a malformed (zero-value) Curve
	}
	return uint8(out)
}

// Describe renders the curve for logs and status output, e.g.
// "soft (γ=0.60, out 1..127)". The preset name is derived from gamma
// (presets have fixed exponents) so the struct stays minimal.
func (c Curve) Describe() string {
	name := "custom"
	switch c.gamma {
	case gammaSoft:
		name = "soft"
	case gammaLinear:
		name = "linear"
	case gammaHard:
		name = "hard"
	}
	return fmt.Sprintf("%s (γ=%.2f, out %d..%d)", name, c.gamma, c.outMin, c.outMax)
}
