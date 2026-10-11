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
