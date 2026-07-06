//! Per-voice oscillator.
//!
//! Stage 3 (`docs/ROADMAP.md` §1.1): each voice carries three of these,
//! each with a selectable waveform (saw | square | pulse), an octave
//! shift, a detune in cents, and a mixer level. The struct holds all
//! three fundsp PolyBLEP generators and enum-dispatches `tick` to the
//! active one — the simplest RT-safe design (no allocation, no boxed
//! trait objects). Switching waveform mid-note freezes the phases of
//! the inactive generators, so a switch may click; that is acceptable
//! and by design for this stage.
//!
//! The pulse waveform's duty cycle is a runtime parameter (fundsp
//! `PolyPulse` takes width as a per-tick input): default 25% — the
//! stage-3 fixed value, so the default render is bit-identical — with
//! the knob clamped to [0.05, 0.95].

use fundsp::audionode::AudioNode;
use fundsp::oscillator::{PolyPulse, PolySaw, PolySquare};

/// Default pulse-wave duty cycle (the stage-3 fixed value — keeping it
/// as the default preserves the regression guarantee).
pub const DEFAULT_PULSE_WIDTH: f32 = 0.25;

/// Selectable oscillator waveform. The `u32` encoding (0/1/2) is the
/// wire format used by the DSP atomics and the C ABI.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum Waveform {
    #[default]
    Saw,
    Square,
    Pulse,
}

impl Waveform {
    /// Decode the atomic/FFI encoding (0 = saw, 1 = square, 2 = pulse).
    pub fn from_u32(v: u32) -> Option<Self> {
        match v {
            0 => Some(Waveform::Saw),
            1 => Some(Waveform::Square),
            2 => Some(Waveform::Pulse),
            _ => None,
        }
    }
}

/// Per-oscillator parameters, pushed per block from the DSP atomics
/// into each voice (same lifecycle as `FilterEnvParams`).
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct OscParams {
    pub wave: Waveform,
    /// Octave shift in -2..=2.
    pub octave: i32,
    /// Detune in cents, -100..=100.
    pub detune_cents: f32,
    /// Mixer level in 0..=1.
    pub level: f32,
}

impl OscParams {
    /// Stage-3 defaults. Osc 1 alone at full level reproduces the
    /// Phase-1 single-saw voice bit-for-bit (regression guarantee); osc
    /// 2 and 3 default to level 0 — silent — but carry the Moog-ish
    /// detune/octave offsets so turning their levels up immediately
    /// sounds right. (§1.4's factory mix 1.0/0.7/0.5 and osc-3 triangle
    /// are *patch* values, applied when the patch loader (§3) lands.)
    pub fn default_bank() -> [OscParams; 3] {
        [
            OscParams {
                wave: Waveform::Saw,
                octave: 0,
                detune_cents: 0.0,
                level: 1.0,
            },
            OscParams {
                wave: Waveform::Saw,
                octave: 0,
                detune_cents: -7.0,
                level: 0.0,
            },
            OscParams {
                wave: Waveform::Saw,
                octave: -1,
                detune_cents: 5.0,
                level: 0.0,
            },
        ]
    }
}

/// Single anti-aliased oscillator. The output is in -1..+1.
pub struct Oscillator {
    saw: PolySaw<f32>,
    square: PolySquare<f32>,
    pulse: PolyPulse<f32>,
    params: OscParams,
    /// Cached `2^(octave + detune_cents/1200)` so `tick` is one
    /// multiply. Exactly 1.0 at octave 0 / detune 0 — the default path
    /// feeds the generator bit-identical frequencies to the Phase 1
    /// single-osc voice.
    pitch_factor: f32,
    /// Pulse-wave duty cycle in [0.05, 0.95], fed to `PolyPulse` as its
    /// width input every tick. Default 0.25 — exactly the old fixed
    /// stage-3 constant, so the default render is bit-identical
    /// (regression guarantee). Only the pulse waveform reads it.
    pulse_width: f32,
}

impl Oscillator {
    /// Construct a fresh oscillator at the given sample rate (Hz). The
    /// phase comes from the generators' `new()` (deterministic for the
    /// default hash) — see Phase 2 if voices should phase-lock instead.
    pub fn new(sample_rate: f32, params: OscParams) -> Self {
        let mut saw = PolySaw::<f32>::new();
        saw.set_sample_rate(sample_rate as f64);
        let mut square = PolySquare::<f32>::new();
        square.set_sample_rate(sample_rate as f64);
        let mut pulse = PolyPulse::<f32>::new();
        pulse.set_sample_rate(sample_rate as f64);
        Self {
            saw,
            square,
            pulse,
            params,
            pitch_factor: Self::pitch_factor(&params),
            pulse_width: DEFAULT_PULSE_WIDTH,
        }
    }

    fn pitch_factor(params: &OscParams) -> f32 {
        ((params.octave as f32) + params.detune_cents * (1.0 / 1200.0)).exp2()
    }

    /// Per-block parameter push. Change-detected so it's free while
    /// knobs are idle. Changing the waveform mid-note may click (the
    /// newly-selected generator resumes from its frozen phase).
    pub fn set_params(&mut self, params: OscParams) {
        if params != self.params {
            self.params = params;
            self.pitch_factor = Self::pitch_factor(&params);
        }
    }

    /// Per-block pulse-width push, mirroring `set_params`. Clamped to
    /// [0.05, 0.95] (extreme widths collapse the pulse into DC-with-
    /// clicks territory). Only audible while the pulse waveform is
    /// selected; the saw/square paths never read it.
    pub fn set_pulse_width(&mut self, width: f32) {
        self.pulse_width = width.clamp(0.05, 0.95);
    }

    /// Pulse width currently programmed into the generator (test probe
    /// for the voice's per-sample width smoothing).
    #[cfg(test)]
    pub(crate) fn pulse_width(&self) -> f32 {
        self.pulse_width
    }

    /// Render one sample at `base_freq_hz * 2^(octave + cents/1200)`.
    pub fn tick(&mut self, base_freq_hz: f32) -> f32 {
        let freq = base_freq_hz * self.pitch_factor;
        match self.params.wave {
            Waveform::Saw => self.saw.tick(&[freq].into())[0],
            Waveform::Square => self.square.tick(&[freq].into())[0],
            Waveform::Pulse => self.pulse.tick(&[freq, self.pulse_width].into())[0],
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn osc(wave: Waveform, octave: i32, detune_cents: f32) -> Oscillator {
        Oscillator::new(
            48_000.0,
            OscParams {
                wave,
                octave,
                detune_cents,
                level: 1.0,
            },
        )
    }

    fn min_max(osc: &mut Oscillator, freq: f32, n: usize) -> (f32, f32) {
        let mut min = f32::INFINITY;
        let mut max = f32::NEG_INFINITY;
        for _ in 0..n {
            let v = osc.tick(freq);
            min = min.min(v);
            max = max.max(v);
        }
        (min, max)
    }

    /// PolyBLEP saw output should span -1..+1 over a few cycles.
    #[test]
    fn saw_spans_bipolar_range() {
        // 4+ cycles of 100 Hz = ~1920 samples @ 48 kHz.
        let (min, max) = min_max(&mut osc(Waveform::Saw, 0, 0.0), 100.0, 2400);
        // PolyBLEP introduces a small amount of overshoot at edges, so we
        // assert "close to ±1" rather than exact equality.
        assert!(min < -0.8, "saw min should be near -1, got {min}");
        assert!(max > 0.8, "saw max should be near +1, got {max}");
    }

    /// Square and pulse also span the bipolar range.
    #[test]
    fn square_and_pulse_span_bipolar_range() {
        for wave in [Waveform::Square, Waveform::Pulse] {
            let (min, max) = min_max(&mut osc(wave, 0, 0.0), 100.0, 2400);
            assert!(min < -0.8, "{wave:?} min should be near -1, got {min}");
            assert!(max > 0.8, "{wave:?} max should be near +1, got {max}");
        }
    }

    /// A 25% pulse spends ~25% of each cycle high — distinguishing it
    /// from the 50%-duty square.
    #[test]
    fn pulse_duty_is_quarter() {
        let mut p = osc(Waveform::Pulse, 0, 0.0);
        let n = 48_000; // 100 cycles of 100 Hz
        let high = (0..n).filter(|_| p.tick(100.0) > 0.0).count();
        let duty = high as f32 / n as f32;
        assert!(
            (duty - 0.25).abs() < 0.02,
            "pulse duty should be ~25%, got {duty}"
        );
    }

    /// `set_pulse_width` retargets the duty cycle: width 0.5 spends
    /// ~50% of each cycle high, width 0.1 spends ~10%.
    #[test]
    fn pulse_width_sets_duty() {
        for width in [0.5f32, 0.1] {
            let mut p = osc(Waveform::Pulse, 0, 0.0);
            p.set_pulse_width(width);
            let n = 48_000; // 100 cycles of 100 Hz
            let high = (0..n).filter(|_| p.tick(100.0) > 0.0).count();
            let duty = high as f32 / n as f32;
            assert!(
                (duty - width).abs() < 0.02,
                "pulse duty should be ~{width}, got {duty}"
            );
        }
    }

    /// `set_pulse_width` clamps to [0.05, 0.95].
    #[test]
    fn set_pulse_width_clamps() {
        let mut p = osc(Waveform::Pulse, 0, 0.0);
        p.set_pulse_width(2.0);
        assert_eq!(p.pulse_width, 0.95);
        p.set_pulse_width(-1.0);
        assert_eq!(p.pulse_width, 0.05);
        p.set_pulse_width(0.5);
        assert_eq!(p.pulse_width, 0.5);
    }

    /// Octave -1 halves the frequency: count rising zero crossings.
    #[test]
    fn octave_shift_scales_frequency() {
        let count_rising = |osc: &mut Oscillator| {
            let mut prev = osc.tick(100.0);
            let mut count = 0;
            for _ in 0..48_000 {
                let v = osc.tick(100.0);
                if prev < 0.0 && v >= 0.0 {
                    count += 1;
                }
                prev = v;
            }
            count
        };
        let base = count_rising(&mut osc(Waveform::Saw, 0, 0.0));
        let down = count_rising(&mut osc(Waveform::Saw, -1, 0.0));
        assert!(
            (base as f32 / down as f32 - 2.0).abs() < 0.1,
            "octave -1 should halve zero crossings: base={base}, down={down}"
        );
    }

    /// The default pitch factor is exactly 1.0 (regression guarantee:
    /// osc 1 at octave 0 / detune 0 feeds the saw bit-identical
    /// frequencies to the Phase 1 voice).
    #[test]
    fn default_pitch_factor_is_exactly_one() {
        let o = osc(Waveform::Saw, 0, 0.0);
        assert_eq!(o.pitch_factor.to_bits(), 1.0_f32.to_bits());
    }

    /// from_u32 round-trips the wire encoding and rejects garbage.
    #[test]
    fn waveform_from_u32() {
        assert_eq!(Waveform::from_u32(0), Some(Waveform::Saw));
        assert_eq!(Waveform::from_u32(1), Some(Waveform::Square));
        assert_eq!(Waveform::from_u32(2), Some(Waveform::Pulse));
        assert_eq!(Waveform::from_u32(3), None);
    }
}
