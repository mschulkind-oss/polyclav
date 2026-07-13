package measure

import (
	"fmt"
	"math"
	"strings"
)

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// finiteLUFSRange returns the min/max LUFS among ms's finite entries
// (ignoring ±Inf/NaN — a true-silence measurement would otherwise
// distort the range). ok is false if no entry is finite.
func finiteLUFSRange(ms []Measurement) (minLUFS, maxLUFS float32, ok bool) {
	for _, m := range ms {
		if math.IsInf(float64(m.LUFS), 0) || math.IsNaN(float64(m.LUFS)) {
			continue
		}
		if !ok || m.LUFS < minLUFS {
			minLUFS = m.LUFS
		}
		if !ok || m.LUFS > maxLUFS {
			maxLUFS = m.LUFS
		}
		ok = true
	}
	return minLUFS, maxLUFS, ok
}

// CheckConsistent reports whether the measurements' loudness spread
// (loudest minus quietest, among finite entries) is within
// toleranceLU — i.e. every patch is within toleranceLU of every other
// patch, not just of a group average. Spread beats a mean/median
// comparison here: with a small group, one real outlier skews a mean
// enough to make well-matched entries look inconsistent too; a direct
// max-min spread doesn't have that failure mode, and it's a more
// literal reading of "about as loud as each other" anyway. Any silent
// (non-finite) entry always fails, regardless of tolerance — it's
// never "about as loud" as an audible one. This is the
// patch-loudness-normalization check (docs/VISION.md): render the same
// short performance through several patches and confirm none of them
// stands out. Always returns a human-readable report, even when the
// check passes, so a caller can log the actual numbers either way.
func CheckConsistent(ms []Measurement, toleranceLU float32) (ok bool, report string) {
	if len(ms) == 0 {
		return true, "loudness consistency: no measurements to compare"
	}
	minLUFS, maxLUFS, haveFinite := finiteLUFSRange(ms)
	spread := maxLUFS - minLUFS
	hasSilence := false
	for _, m := range ms {
		if math.IsInf(float64(m.LUFS), -1) {
			hasSilence = true
		}
	}
	spreadExceeded := haveFinite && spread > toleranceLU
	ok = haveFinite && !hasSilence && !spreadExceeded

	var sb strings.Builder
	fmt.Fprintf(&sb, "loudness consistency (tolerance %.1f LU; spread %.2f LU across %d patches):\n", toleranceLU, spread, len(ms))
	for _, m := range ms {
		flag := ""
		switch {
		case math.IsInf(float64(m.LUFS), -1):
			flag = "  <-- SILENT"
		case spreadExceeded && (m.LUFS == minLUFS || m.LUFS == maxLUFS):
			flag = "  <-- SPREAD OUTLIER"
		}
		fmt.Fprintf(&sb, "  %-24s %8.2f LUFS%s\n", m.Label, m.LUFS, flag)
	}
	return ok, sb.String()
}

// CheckGradual reports whether consecutive measurements — assumed
// meaningfully ordered, e.g. a parameter sweep from 0 to 1 — never
// jump by more than maxStepLU in LUFS between neighbors. This is the
// "is a knob smooth, not on/off" check: the same shape as the
// drive-pedal regression this package exists to prevent
// (docs/VISION.md), generalized to any labeled sweep.
func CheckGradual(ms []Measurement, maxStepLU float32) (ok bool, report string) {
	if len(ms) < 2 {
		return true, "loudness gradualness: fewer than 2 measurements, nothing to compare"
	}
	ok = true
	var sb strings.Builder
	fmt.Fprintf(&sb, "loudness gradualness (max step %.1f LU):\n", maxStepLU)
	for i := 1; i < len(ms); i++ {
		step := ms[i].LUFS - ms[i-1].LUFS
		flag := ""
		if abs32(step) > maxStepLU {
			ok = false
			flag = "  <-- JUMP"
		}
		fmt.Fprintf(&sb, "  %-20s -> %-20s  %+7.2f LU%s\n", ms[i-1].Label, ms[i].Label, step, flag)
	}
	return ok, sb.String()
}

// CheckBounded reports whether every measurement's peak stays at or
// under maxPeakDBFS — a safety/sanity check independent of loudness
// consistency (e.g. does cranking a distortion parameter ever risk
// clipping upstream of the limiter).
func CheckBounded(ms []Measurement, maxPeakDBFS float32) (ok bool, report string) {
	ok = true
	var sb strings.Builder
	fmt.Fprintf(&sb, "peak bound (max %.1f dBFS):\n", maxPeakDBFS)
	for _, m := range ms {
		flag := ""
		if m.PeakDBFS > maxPeakDBFS {
			ok = false
			flag = "  <-- OVER"
		}
		fmt.Fprintf(&sb, "  %-24s %8.2f dBFS%s\n", m.Label, m.PeakDBFS, flag)
	}
	return ok, sb.String()
}
