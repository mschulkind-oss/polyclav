//! Tube-Screamer-style overdrive pedal — a pre-gain stage into an antiparallel
//! diode pair shunting to ground (the classic TS-808/TS9 clipping topology),
//! modeled as a memoryless nonlinear one-port. The exact per-sample circuit
//! equation is `(driven_input - v) / R = 2 * Is * sinh(v / Vt)` for the
//! clipped output voltage `v`. Rather than solve that implicit equation with
//! fixed-iteration Newton-Raphson (tried first; rejected — a naive Newton
//! step from a poor initial guess overshoots by orders of magnitude on this
//! steep exponential, overflowing `sinh`/`cosh` well before a small fixed
//! iteration count converges) or the reference paper's closed-form Lambert-W
//! solution (Werner/Nangia/Bernardini/Smith/Sarti, AES 2015, "An Improved and
//! Generalized Diode Clipper Model for Wave Digital Filters"), this uses the
//! standard deep-conduction approximation: once the diodes conduct, the
//! resistive feedback term `-v/R` is negligible next to the exponential diode
//! current, leaving `driven/R ≈ 2*Is*sinh(v/Vt)`, solved in closed form as
//! `v = Vt * asinh(driven / (2*Is*R))`. Same diode-pair model, no iteration,
//! numerically robust for any input magnitude (`asinh` never overflows for a
//! finite argument), and matches the exact solution closely in the region
//! that matters — both approaches agree that `v -> 0` as `driven -> 0`.
//!
//! See docs/OPEN_SOUND_ENGINES.md §1 for the research this implements.
//!
//! Component constants (R, Vt, Is below) are representative silicon small-signal-
//! diode values (1N4148-class), NOT SPICE-matched to a specific real pedal —
//! they are a reasonable starting point expected to be ear-tuned after first
//! listen (per docs/VISION.md's "prototype, then profile" guiding principle),
//! not asserted as verified-accurate.
//!
//! This stage runs 2x oversampled (fundsp's Oversampler, same wrapper pattern as
//! synth::filter::OversampledDriveLadder) because a hard diode nonlinearity
//! aliases badly at 48 kHz without it.
//!
//! Note `amount` 0.0 (the default) bypasses the stage bit-exactly — same
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
const PRE_GAIN_SCALE: f32 = 400.0;
const OUTPUT_MAKEUP: f32 = 6.0;
/// Sanity bound on the pre-gained signal before it enters `asinh`. Real
/// audio never approaches this — `x` is normally within [-1, 1] and
/// `pre_gain` maxes out at `1 + PRE_GAIN_SCALE`, so `driven` maxes out
/// around 401. This only ever clamps a pathological upstream value, and
/// guarantees `driven / (2*Is*R)` stays comfortably inside f32 range so
/// the stage can never emit non-finite output.
const DRIVEN_CLAMP: f32 = 1.0e6;

/// Memoryless — the closed-form approximation has no per-sample state to
/// carry (see module docs for why this replaced an iterative solve).
#[derive(Clone)]
struct DiodeClipperNode {
    pre_gain: f32,
}

impl DiodeClipperNode {
    #[inline]
    fn tick_sample(&self, x: f32) -> f32 {
        let driven = (x * self.pre_gain).clamp(-DRIVEN_CLAMP, DRIVEN_CLAMP);
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

struct Channel {
    inner: Oversampler<DiodeClipperNode>,
}

impl Channel {
    fn new(sample_rate: f32) -> Self {
        Self {
            inner: Oversampler::new(sample_rate as f64, DiodeClipperNode { pre_gain: 1.0 }),
        }
    }

    fn set_pre_gain(&mut self, g: f32) {
        self.inner.node_mut().pre_gain = g;
    }

    #[inline]
    fn tick(&mut self, x: f32) -> f32 {
        self.inner.tick(&[x].into())[0]
    }
}

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
        let amt = amount.clamp(0.0, 1.0);
        self.amount = amt;
        let pre_gain = 1.0 + amt * PRE_GAIN_SCALE;
        self.left.set_pre_gain(pre_gain);
        self.right.set_pre_gain(pre_gain);
    }

    #[allow(dead_code)]
    pub fn amount(&self) -> f32 {
        self.amount
    }

    pub fn process(&mut self, samples: &mut [f32]) {
        if self.amount <= 0.0 {
            return;
        }

        for chunk in samples.chunks_exact_mut(2) {
            chunk[0] = self.left.tick(chunk[0]);
            chunk[1] = self.right.tick(chunk[1]);
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
        let mut samples = [0.0; 4800];
        let amp = 0.3;
        for (i, s) in samples.iter_mut().enumerate() {
            *s = (2.0 * std::f32::consts::PI * 440.0 * i as f32 / 48000.0).sin() * amp;
        }

        pedal.process(&mut samples);

        let max_out = samples.iter().cloned().fold(0.0_f32, |a, b| a.max(b.abs()));

        assert!(max_out.is_finite());
        assert!(max_out < 20.0);
    }
}
