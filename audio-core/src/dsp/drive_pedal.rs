//! Tube-Screamer-style overdrive pedal — a pre-gain stage into an antiparallel
//! diode pair shunting to ground (the classic TS-808/TS9 clipping topology),
//! modeled as a memoryless nonlinear one-port. The exact per-sample circuit
//! equation is `(driven_input - v) / R = 2 * Is * sinh(v / Vt)` for the
//! clipped output voltage `v`, solved here via the closed-form deep-conduction
//! approximation `v = Vt * asinh(driven / (2*Is*R))` (see git history for the
//! two solves this replaced — an iterative Newton-Raphson attempt diverged,
//! and this crate never implemented the reference paper's Lambert-W solve).
//!
//! **Architecture note (v2, post-first-listen fix):** `R`/`Vt`/`Is` for a
//! real silicon diode pair are so small relative to a normalized [-1, 1]
//! float signal that the diode pair is *always* deep in conduction for any
//! audible input — there is no "clean" operating point to sweep the knob
//! into from below. v1 tried to control drive by scaling `pre_gain` with
//! `amount`, which meant even 1% of knob travel already produced a
//! maximally-saturated, very loud signal (reported after first listen — an
//! on/off knob, not a sweep). v2 fixes this at the architecture level
//! instead of re-tuning constants: the diode pair always runs at a FIXED,
//! fully-driven gain (`FIXED_DRIVE_GAIN`), and `amount` instead controls a
//! linear wet/dry crossfade between the clean input and that fixed-character
//! wet signal. This is smooth by construction — a linear blend is
//! continuous in `amount` regardless of how nonlinear the wet path is — and
//! is the standard way software distortion effects make a "drive" knob feel
//! musical rather than binary. `OUTPUT_MAKEUP` is calibrated against
//! `dsp::loudness::measure_lufs` (see the crate's loudness-invariant tests)
//! so the fully-wet signal lands close to the dry signal's loudness rather
//! than just getting louder as the knob turns.
//!
//! See docs/OPEN_SOUND_ENGINES.md §1 for the research this implements, and
//! docs/VISION.md for the wider loudness/invariant-testing initiative this
//! fix is part of.
//!
//! Component constants (R, Vt, Is) are representative silicon
//! small-signal-diode values (1N4148-class), not SPICE-matched to a
//! specific real pedal.
//!
//! Runs 2x oversampled (fundsp's Oversampler, same wrapper pattern as
//! synth::filter::OversampledDriveLadder) because a hard diode nonlinearity
//! aliases badly at 48 kHz without it. The wet/dry mix itself happens at
//! the base rate, outside the oversampled node — it's a linear operation,
//! so mixing after decimation is equivalent to mixing inside, and it keeps
//! the dry path free of the halfband filter's latency/response entirely.
//!
//! `amount` 0.0 (the default) bypasses the stage bit-exactly — same
//! regression-safety convention used by every other knob in this codebase
//! (e.g. native_drive, comp_amount defaulting to 0.0 = bypass).

use fundsp::audionode::AudioNode;
use fundsp::oversample::Oversampler;
use fundsp::typenum::U1;
use fundsp::Frame;

const DEFAULT_SAMPLE_RATE: f32 = 48_000.0;
const THERMAL_VOLTAGE: f32 = 0.02585;
const SATURATION_CURRENT: f32 = 2.52e-9;
const INPUT_RESISTANCE: f32 = 4_700.0;
/// The wet path's fixed pre-gain — always fully driven; `amount` controls
/// how much of this (fixed-character) wet signal is blended in, not how
/// hard the diode pair is driven. See the module doc comment.
const FIXED_DRIVE_GAIN: f32 = 400.0;
/// Calibrated against `dsp::loudness::measure_lufs` so a fully-wet
/// (`amount = 1.0`) held note lands within about 1 LU of the same note
/// dry — cranking the drive changes character, not just loudness. See
/// `lib.rs`'s `drive_pedal_loudness_*` tests, which pin this.
const OUTPUT_MAKEUP: f32 = 0.236;
/// Sanity bound on the pre-gained signal before it enters `asinh`. Real
/// audio never approaches this — `x` is normally within [-1, 1] and
/// `FIXED_DRIVE_GAIN` is a constant, so `driven` never exceeds a few
/// hundred. This only ever clamps a pathological upstream value, and
/// guarantees `driven / (2*Is*R)` stays comfortably inside f32 range so
/// the stage can never emit non-finite output.
const DRIVEN_CLAMP: f32 = 1.0e6;

/// Memoryless — the closed-form approximation has no per-sample state to
/// carry.
#[derive(Clone)]
struct DiodeClipperNode;

impl DiodeClipperNode {
    #[inline]
    fn tick_sample(&self, x: f32) -> f32 {
        let driven = (x * FIXED_DRIVE_GAIN).clamp(-DRIVEN_CLAMP, DRIVEN_CLAMP);
        let v = THERMAL_VOLTAGE * (driven / (2.0 * SATURATION_CURRENT * INPUT_RESISTANCE)).asinh();
        v * OUTPUT_MAKEUP
    }
}

impl AudioNode for DiodeClipperNode {
    const ID: u64 = 0x706F_6C79_5F6F_6432;
    type Inputs = U1;
    type Outputs = U1;

    fn reset(&mut self) {
        // No per-sample state to clear — see the struct doc comment.
    }

    /// This node is stateless with respect to sample rate — the closed-form
    /// solve has no per-sample time constant to retune, unlike DriveLadder's
    /// Moog filter.
    fn set_sample_rate(&mut self, _sample_rate: f64) {}

    #[inline]
    fn tick(&mut self, input: &Frame<f32, Self::Inputs>) -> Frame<f32, Self::Outputs> {
        [self.tick_sample(input[0])].into()
    }
}

/// One channel's 2x oversampled, always-fully-driven diode clipper. The
/// wet/dry `mix` is applied here, at the base rate, after decimation.
struct Channel {
    inner: Oversampler<DiodeClipperNode>,
}

impl Channel {
    fn new(sample_rate: f32) -> Self {
        Self {
            inner: Oversampler::new(sample_rate as f64, DiodeClipperNode),
        }
    }

    #[inline]
    fn tick(&mut self, x: f32, mix: f32) -> f32 {
        let wet = self.inner.tick(&[x].into())[0];
        x + mix * (wet - x)
    }
}

/// Drive pedal effect: two independent [`Channel`]s (L/R), each a 2×
/// oversampled diode-pair clipper crossfaded with the dry signal by
/// `amount`. `amount` in [0,1]; 0.0 bypasses bit-exactly.
pub struct DrivePedal {
    left: Channel,
    right: Channel,
    amount: f32,
}

impl DrivePedal {
    pub fn new(sample_rate: f32) -> Self {
        Self {
            left: Channel::new(sample_rate),
            right: Channel::new(sample_rate),
            amount: 0.0,
        }
    }

    pub fn set_amount(&mut self, amount: f32) {
        self.amount = amount.clamp(0.0, 1.0);
    }

    #[allow(dead_code)]
    pub fn amount(&self) -> f32 {
        self.amount
    }

    pub fn process(&mut self, samples: &mut [f32]) {
        if self.amount <= 0.0 {
            return;
        }

        let mix = self.amount;
        for chunk in samples.chunks_exact_mut(2) {
            chunk[0] = self.left.tick(chunk[0], mix);
            chunk[1] = self.right.tick(chunk[1], mix);
        }
    }
}

impl Default for DrivePedal {
    fn default() -> Self {
        Self::new(DEFAULT_SAMPLE_RATE)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sine(amp: f32, freq: f32, n: usize) -> Vec<f32> {
        (0..n)
            .map(|i| (2.0 * std::f32::consts::PI * freq * i as f32 / 48000.0).sin() * amp)
            .collect()
    }

    #[test]
    fn test_new_no_panic() {
        let _ = DrivePedal::new(48_000.0);
    }

    #[test]
    fn test_amount_zero_is_bit_transparent() {
        let mut pedal = DrivePedal::new(48_000.0);
        let mut samples = [0.0; 480];
        let amp = 0.5;
        for (i, s) in samples.iter_mut().enumerate() {
            *s = (2.0 * std::f32::consts::PI * 440.0 * i as f32 / 48000.0).sin() * amp;
        }

        let samples_before = samples;
        pedal.process(&mut samples);

        for (before, after) in samples_before.iter().zip(samples.iter()) {
            assert_eq!(before, after);
        }
    }

    #[test]
    fn test_silence_in_silence_out() {
        let mut pedal = DrivePedal::new(48_000.0);
        pedal.set_amount(1.0);
        let mut samples = [0.0; 1024];
        pedal.process(&mut samples);
        for &s in &samples {
            assert!(s.abs() < 1e-6);
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_no_nan_or_inf_on_loud_signal() {
        let mut pedal = DrivePedal::new(48_000.0);
        pedal.set_amount(1.0);
        let mut samples = vec![0.0; 4096];
        for (i, s) in samples.iter_mut().enumerate() {
            *s = if i % 2 == 0 { 1.0 } else { -1.0 };
        }
        pedal.process(&mut samples);
        for &s in &samples {
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_set_amount_clamps() {
        let mut pedal = DrivePedal::new(48_000.0);
        pedal.set_amount(-1.0);
        assert_eq!(pedal.amount(), 0.0);
        pedal.set_amount(2.0);
        assert_eq!(pedal.amount(), 1.0);
    }

    #[test]
    fn test_amount_one_saturates() {
        let mut pedal = DrivePedal::new(48_000.0);
        pedal.set_amount(1.0);
        let mut samples = sine(0.3, 440.0, 4800);

        pedal.process(&mut samples);

        let max_out = samples.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));

        assert!(max_out.is_finite());
        assert!(max_out < 20.0);
    }

    /// The regression the "on/off knob" bug report pins: a small amount
    /// (1%) must sound close to dry, not already maximally driven. Uses
    /// a mono-summed proxy for "how different from dry" (mean absolute
    /// difference) rather than pulling in the loudness meter here —
    /// lib.rs's loudness-invariant tests cover the perceptual/LUFS side
    /// of this same regression against the full render chain.
    #[test]
    fn test_small_amount_stays_close_to_dry() {
        let dry = sine(0.3, 440.0, 4800);

        let mut wet_1pct = dry.clone();
        let mut pedal = DrivePedal::new(48_000.0);
        pedal.set_amount(0.01);
        pedal.process(&mut wet_1pct);

        let mut wet_full = dry.clone();
        let mut pedal_full = DrivePedal::new(48_000.0);
        pedal_full.set_amount(1.0);
        pedal_full.process(&mut wet_full);

        let mean_abs_diff = |a: &[f32], b: &[f32]| -> f32 {
            a.iter()
                .zip(b.iter())
                .map(|(x, y)| (x - y).abs())
                .sum::<f32>()
                / a.len() as f32
        };

        let diff_1pct = mean_abs_diff(&dry, &wet_1pct);
        let diff_full = mean_abs_diff(&dry, &wet_full);

        assert!(
            diff_1pct < diff_full * 0.1,
            "1% drive should be much closer to dry than full drive: \
             diff_1pct={diff_1pct}, diff_full={diff_full}"
        );
    }
}
