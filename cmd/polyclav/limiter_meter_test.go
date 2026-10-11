package main

import (
	"math"
	"testing"
	"time"
)

func TestLimiterReductionDB(t *testing.T) {
	// Unity and "no reading" (0 = no callback yet) both mean no reduction.
	if got := limiterReductionDB(1.0); got != 0 {
		t.Errorf("unity gain = %v dB, want 0", got)
	}
	if got := limiterReductionDB(0); got != 0 {
		t.Errorf("zero gain (unset metric) = %v dB, want 0", got)
	}
	// Half gain is ~6.02 dB of reduction.
	if got := limiterReductionDB(0.5); math.Abs(float64(got)-6.0206) > 0.001 {
		t.Errorf("half gain = %v dB, want ~6.0206", got)
	}
}

func TestLimiterScreenLabel(t *testing.T) {
	if got := limiterScreenLabel(0.2); got != "" {
		t.Errorf("below threshold = %q, want empty", got)
	}
	if got := limiterScreenLabel(3.44); got != "LIMIT 3.4 dB" {
		t.Errorf("label = %q, want %q", got, "LIMIT 3.4 dB")
	}
	if n := len(limiterScreenLabel(18.0)); n > 16 {
		t.Errorf("label %q is %d chars, over the 16-char screen line", limiterScreenLabel(18.0), n)
	}
}

func TestLimiterMeterHoldsPeak(t *testing.T) {
	m := &limiterMeter{}
	base := time.Unix(0, 0)

	// A hit sets the peak (~6.02 dB from gain 0.5).
	if got := m.observe(0.5, base); math.Abs(float64(got)-6.0206) > 0.001 {
		t.Fatalf("first sample = %v dB, want ~6.0206", got)
	}
	// A quieter sample inside the hold window keeps the peak.
	if got := m.observe(0.9, base.Add(limiterHold/2)); got != m.peakDB {
		t.Errorf("held peak = %v, want %v", got, m.peakDB)
	}
	// Past the hold with no new peak, it follows the sample down to 0.
	if got := m.observe(1.0, base.Add(limiterHold*2)); got != 0 {
		t.Errorf("after hold = %v, want 0", got)
	}
	// A new, smaller peak restarts the window.
	if got := m.observe(0.7071, base.Add(limiterHold*3)); math.Abs(float64(got)-3.01) > 0.05 {
		t.Errorf("new peak = %v dB, want ~3.01", got)
	}
}
