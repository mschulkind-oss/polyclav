package main

import (
	"fmt"
	"math"
	"time"
)

// limiterEngageDB is the gain reduction at or above which the screen shows
// the limiter marker. The limiter's soft knee starts ~3 dB below the ceiling,
// but the reduction it applies near the knee is inaudible; 0.5 dB is
// "actually compressing", so the marker means something rather than blinking
// on every loud note that merely grazes the knee.
const limiterEngageDB = 0.5

// limiterHold keeps the marker up this long after the last sample that
// reached its peak, so a peaking passage reads as a steady "you're in the
// red" rather than a flicker.
const limiterHold = 700 * time.Millisecond

// limiterIndicatorRefresh is the minimum spacing between screen writes while
// the limiter stays engaged — limits SysEx traffic without letting the marker
// disappear (a patch switch or knob popup that repainted the screen is
// re-asserted on the next tick past this window).
const limiterIndicatorRefresh = 250 * time.Millisecond

// limiterMeter peak-holds limiter gain reduction for the screen indicator.
// Reduction is derived from the limiter's worst-case gain for one audio
// callback (1.0 = no reduction), which audio-core reports in thousandths
// (see PolyclavAudioMetrics.LimiterMinGainMilli).
type limiterMeter struct {
	peakDB float32
	peakAt time.Time
}

// limiterReductionDB converts a limiter gain in (0, 1] to gain reduction in
// dB. A zero or out-of-range gain — "no callback yet", or an unset metric —
// reads as no reduction.
func limiterReductionDB(minGain float32) float32 {
	if !(minGain > 0) || minGain >= 1 {
		return 0
	}
	return float32(-20 * math.Log10(float64(minGain)))
}

// observe folds one metrics sample into the meter and returns the held peak
// reduction in dB. The peak holds for limiterHold after the last sample that
// matched or exceeded it; after that it follows the current sample down.
func (m *limiterMeter) observe(minGain float32, now time.Time) float32 {
	db := limiterReductionDB(minGain)
	if db >= m.peakDB {
		m.peakDB = db
		m.peakAt = now
	} else if now.Sub(m.peakAt) >= limiterHold {
		m.peakDB = db
	}
	return m.peakDB
}

// limiterScreenLabel formats the screen's line-2 marker for a peak gain
// reduction, or "" when it is below the engage threshold (nothing to show).
// 16-char line budget: "LIMIT 12.3 dB" is 13.
func limiterScreenLabel(peakDB float32) string {
	if peakDB < limiterEngageDB {
		return ""
	}
	return fmt.Sprintf("LIMIT %.1f dB", peakDB)
}

// limiterPadFlash is the pure state for the patch-pad clip flash: which row-0
// column currently shows red, and when it was last written. main maps its
// decisions onto SetPadColor and the row repaint.
//
// Red is re-asserted every limiterIndicatorRefresh while engaged so a
// patch/bank repaint during a long clip can't leave the pad dark; it is
// released (whole row repainted to configured colors) as soon as the clip
// clears.
//
// step folds one poll and reports whether to repaint the row and which column
// to set red (-1 for none). Both can be set in the same call when the loaded
// patch moves to a different pad — repaint first, then red.
type limiterPadFlash struct {
	lit bool
	col int
	at  time.Time
}

func (f *limiterPadFlash) step(engaged bool, col int, now time.Time) (restoreRow bool, setRed int) {
	if !engaged || col < 0 {
		if !f.lit {
			return false, -1
		}
		f.lit = false
		return true, -1
	}
	if !f.lit || col != f.col {
		restoreRow = f.lit
		f.lit = true
		f.col = col
		f.at = now
		return restoreRow, col
	}
	if now.Sub(f.at) < limiterIndicatorRefresh {
		return false, -1
	}
	f.at = now
	return false, col
}
