// Package velocity remaps note velocity through a gamma (power) curve
// or a piecewise-linear point curve, with an output clamp, per
// docs/VELOCITY_CURVES.md (v1 gamma, v2 control points).
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

// Curve is an immutable velocity remap, with v==0 passed through
// untouched. Two shapes share the type so callers swap either kind
// atomically on patch select:
//
//   - gamma (points == nil): out(v) = clamp(round(127·(v/127)^γ), outMin, outMax)
//   - points (points != nil): linear interpolation between control points,
//     rounded, then the same clamp
//
// Value semantics still hold for sharing: the points slice is private,
// defensively copied at construction, and never written afterwards, so a
// copied Curve is safe to use concurrently.
type Curve struct {
	gamma  float32
	outMin uint8
	outMax uint8
	// points is the piecewise-linear control-point set ([x, y] pairs),
	// nil for gamma curves. Invariants established by NewFromPoints:
	// 2..16 entries, first [0,0], last x 127, xs strictly increasing,
	// ys non-decreasing — Apply relies on them without rechecking.
	points [][2]uint8
}

// Control-point count bounds. Two points is the minimum that spans
// 0..127; sixteen keeps the set hand-editable in TOML and draggable in
// the web UI's curve editor — a raw 128-entry table is deliberately
// rejected (see docs/VELOCITY_CURVES.md).
const (
	minPoints = 2
	maxPoints = 16
)

// NewFromPoints builds a piecewise-linear curve from [x, y] control
// points (the v2 "draggable curve" model). outMin==0 defaults to 1 and
// outMax==0 defaults to 127, matching New.
//
// Validation guards the invariants Apply depends on: 2..16 points; the
// first point exactly [0,0] (so NoteOn vel 0 stays NoteOff and every
// v>=1 has a surrounding segment); the last x == 127 (full input
// coverage); xs strictly increasing (segments are well-defined); ys
// non-decreasing and <=127 (the mapping is monotonic and in MIDI range).
func NewFromPoints(points [][2]uint8, outMin, outMax uint8) (Curve, error) {
	if len(points) < minPoints || len(points) > maxPoints {
		return Curve{}, fmt.Errorf("velocity: need %d..%d points, got %d", minPoints, maxPoints, len(points))
	}
	if points[0] != [2]uint8{0, 0} {
		return Curve{}, fmt.Errorf("velocity: first point must be [0, 0], got [%d, %d] (NoteOn vel 0 must stay NoteOff)", points[0][0], points[0][1])
	}
	if last := points[len(points)-1]; last[0] != 127 {
		return Curve{}, fmt.Errorf("velocity: last point x must be 127, got %d", last[0])
	}
	for i, p := range points {
		if p[1] > 127 {
			return Curve{}, fmt.Errorf("velocity: point %d y %d out of range 0..127", i, p[1])
		}
		if i == 0 {
			continue
		}
		if p[0] <= points[i-1][0] {
			return Curve{}, fmt.Errorf("velocity: point %d x %d must be > previous x %d (strictly increasing)", i, p[0], points[i-1][0])
		}
		if p[1] < points[i-1][1] {
			return Curve{}, fmt.Errorf("velocity: point %d y %d must be >= previous y %d (non-decreasing)", i, p[1], points[i-1][1])
		}
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
	// Defensive copy: callers typically pass config-owned slices, and a
	// later config mutation must not warp an installed curve.
	pts := make([][2]uint8, len(points))
	copy(pts, points)
	return Curve{gamma: gammaLinear, outMin: outMin, outMax: outMax, points: pts}, nil
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
// non-decreasing over 0..127 for both curve shapes: the power function
// is monotonic for any γ>0, piecewise-linear interpolation over
// non-decreasing ys is monotonic, and rounding and clamping preserve
// that.
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
	var out float64
	if c.points != nil {
		out = math.Round(c.interpolate(v))
	} else {
		out = math.Round(127 * math.Pow(float64(v)/127, float64(c.gamma)))
	}
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

// interpolate maps v through the control points by linear interpolation
// on the segment containing it. NewFromPoints guarantees coverage of
// 1..127 (first x is 0, last x is 127, xs strictly increasing), so a
// surrounding segment always exists.
func (c Curve) interpolate(v uint8) float64 {
	for i := 1; i < len(c.points); i++ {
		if v > c.points[i][0] {
			continue
		}
		x0, y0 := float64(c.points[i-1][0]), float64(c.points[i-1][1])
		x1, y1 := float64(c.points[i][0]), float64(c.points[i][1])
		return y0 + (y1-y0)*(float64(v)-x0)/(x1-x0)
	}
	// Unreachable for a NewFromPoints-built curve; return the top point
	// rather than panic if an invariant is ever broken.
	return float64(c.points[len(c.points)-1][1])
}

// Describe renders the curve for logs and status output, e.g.
// "soft (γ=0.60, out 1..127)" or "points[4] (out 1..127)". The gamma
// preset name is derived from gamma (presets have fixed exponents) so
// the struct stays minimal.
func (c Curve) Describe() string {
	if c.points != nil {
		return fmt.Sprintf("points[%d] (out %d..%d)", len(c.points), c.outMin, c.outMax)
	}
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
