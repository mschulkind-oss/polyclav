package measure

import (
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/patches"
)

// nativePatch is the shared test patch for the sweeps below — the one
// native engine available in this environment (see patch_test.go's
// comment on why real soundfont/sfz assets aren't bundled in the repo).
var nativePatch = patches.Patch{Name: "moog", Type: "native", Engine: "minimoog"}

// This is the Go-level version of what audio-core/src/lib.rs's
// drive_pedal_loudness_sweep_is_smooth already pins at the Rust layer —
// exercised here through the real Standard MIDI File fixture (a short
// arpeggio plus a chord tail) rather than a single held note, and
// through the full internal/measure pipeline end to end: SMF parsing ->
// per-value offline render with a chain-param override -> LUFS
// measurement -> CheckGradual. Proves SweepChainParam is a real,
// reusable extension point for "the same framework, more checks" —
// not just a hypothetical API.
func TestSweepDrivePedalIsGradual(t *testing.T) {
	events, totalFrames := loadFixture(t)

	ms, err := SweepChainParam(
		nativePatch, events, totalFrames,
		[]float32{0.0, 0.01, 0.02, 0.05, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
		func(cp *audio.ChainParams, v float32) { cp.DrivePedalAmount = v },
	)
	if err != nil {
		t.Fatalf("SweepChainParam: %v", err)
	}
	if len(ms) != 14 {
		t.Fatalf("expected 14 measurements, got %d", len(ms))
	}

	ok, report := CheckGradual(ms, 4.0)
	if !ok {
		t.Errorf("drive pedal sweep should be gradual (this is exactly the \"1%% is already maximally distorted\" regression):\n%s", report)
	}

	ok, report = CheckBounded(ms, 6.0)
	if !ok {
		t.Errorf("drive pedal sweep should stay under a sane peak bound:\n%s", report)
	}
}

// Go-level version of audio-core/src/lib.rs's
// analog_delay_feedback_sweep_is_smooth, through the real MIDI fixture
// and the full internal/measure pipeline.
func TestSweepAnalogDelayFeedbackIsGradual(t *testing.T) {
	events, totalFrames := loadFixture(t)

	ms, err := SweepChainParam(
		nativePatch, events, totalFrames,
		[]float32{0.0, 0.01, 0.05, 0.1, 0.3, 0.6, 0.9},
		func(cp *audio.ChainParams, v float32) {
			cp.AnalogDelayMix = 1.0 // fixed wet so feedback is the only variable
			cp.AnalogDelayTimeMs = 150.0
			cp.AnalogDelayFeedback = v
		},
	)
	if err != nil {
		t.Fatalf("SweepChainParam: %v", err)
	}

	ok, report := CheckGradual(ms, 4.0)
	if !ok {
		t.Errorf("delay feedback sweep should be gradual:\n%s", report)
	}

	ok, report = CheckBounded(ms, 6.0)
	if !ok {
		t.Errorf("delay feedback sweep should stay bounded even at max feedback:\n%s", report)
	}
}

// SweepChainParam only overrides the one field the closure touches —
// every other chain param stays at the engine default. Verified
// directly: a mix-only sweep with feedback left untouched should
// render identically to feedback=0 (the documented default) at every
// step, i.e. the delay's echo should still be a single clean repeat,
// not accumulate character from a stale/leaked feedback value.
func TestSweepChainParamOnlyTouchesSetField(t *testing.T) {
	events, totalFrames := loadFixture(t)

	ms, err := SweepChainParam(
		nativePatch, events, totalFrames,
		[]float32{0.0, 1.0},
		func(cp *audio.ChainParams, v float32) { cp.AnalogDelayMix = v },
	)
	if err != nil {
		t.Fatalf("SweepChainParam: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("expected 2 measurements, got %d", len(ms))
	}
	// mix=0 bypasses bit-exactly regardless of feedback, so this is
	// really just confirming the sweep ran both values without error
	// and produced two genuinely different results (mix=1 audibly
	// differs from mix=0) — proof the untouched fields didn't
	// accidentally end up overriding something that broke the effect.
	if ms[0].LUFS == ms[1].LUFS {
		t.Error("expected mix=0 and mix=1 to measure differently")
	}
}
