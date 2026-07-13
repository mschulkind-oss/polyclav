//! Analog-style (BBD/bucket-brigade-emulating) delay pedal. Signal flow
//! mirrors a real BBD delay (MXR Carbon Copy, Boss DM-2, EHX Deluxe Memory
//! Man): a clean dry path plus an independent wet/delay path, mixed at the
//! output by `mix`. The "analog warmth" — the thing that makes these
//! pedals sound different from a pristine digital delay — lives *inside
//! the feedback loop*, not on the dry path or even on a single pass
//! through the delay: every real BBD chip compands (compresses signal in,
//! expands it out) to fit its limited dynamic range, and a delay's
//! repeats each take another pass through that nonlinearity, so echoes
//! get progressively warmer/darker/softer the more they repeat. This
//! module reproduces that by applying a one-pole lowpass (bandwidth loss)
//! and [`crate::dsp::saturate::diode_clip`] (the same soft-clipper
//! [`crate::dsp::DrivePedal`] uses, tuned far gentler here) to the
//! feedback path specifically — the first echo (`feedback = 0`, a single
//! slapback) is clean; only the accumulating repeats pick up character.
//!
//! Both `mix` (0 = bypass, dry-only) and `feedback` (repeats) are linear
//! knobs blended the same bit-exact-at-zero way as
//! [`crate::dsp::DrivePedal`]'s `amount`. `time_ms` sets the delay length,
//! clamped to a BBD-plausible range.
//!
//! **Why this can't run away even at high feedback:** `diode_clip` is a
//! logarithmic (asinh-based) compressor — its output grows only as
//! `ln(input)`, never linearly, so no matter how loud a repeat gets before
//! the next pass, the *next* pass's output is bounded to roughly the same
//! narrow range. The loop is therefore structurally self-limiting
//! (bounded-input-bounded-output) regardless of `feedback`, the same
//! reason a real companded BBD delay doesn't blow up as you turn up
//! regeneration — it just settles into a bounded, decaying (or, right at
//! the edge, sustained) pattern rather than diverging. `MAX_FEEDBACK`
//! still caps below unity so the pedal doesn't cross into deliberate
//! self-oscillation, which is a distinct, not-yet-built feature, not an
//! accident. Pinned by `analog_delay_repeats_stay_bounded` below —
//! verified empirically, not just argued.

use super::saturate::diode_clip;

const MIN_DELAY_MS: f32 = 1.0;
const MAX_DELAY_MS: f32 = 1_000.0;
const DEFAULT_TIME_MS: f32 = 300.0;
/// Capped below unity so the pedal stays a delay, not a deliberate
/// self-oscillator — see the module doc comment.
const MAX_FEEDBACK: f32 = 0.9;
/// One-pole lowpass cutoff applied to the feedback path each pass —
/// vintage-BBD-plausible bandwidth loss; repeats get audibly darker as
/// they accumulate passes through this filter.
const FEEDBACK_LOWPASS_HZ: f32 = 3_500.0;
/// Far gentler than DrivePedal's FIXED_DRIVE_GAIN (400.0) — this is
/// meant to add warmth to repeats, not turn the delay into a fuzz box.
const FEEDBACK_DRIVE_GAIN: f32 = 6.0;
/// Calibrated against `dsp::loudness::measure_lufs` (see
/// `lib.rs`'s `analog_delay_*` tests) so repeats settle at a sensible,
/// bounded level rather than an arbitrary one.
const FEEDBACK_MAKEUP: f32 = 0.55;

struct OnePoleLowpass {
    coeff: f32,
    state: f32,
}

impl OnePoleLowpass {
    fn new(cutoff_hz: f32, sample_rate: f32) -> Self {
        let coeff = 1.0 - (-2.0 * std::f32::consts::PI * cutoff_hz / sample_rate).exp();
        Self { coeff, state: 0.0 }
    }

    #[inline]
    fn tick(&mut self, x: f32) -> f32 {
        self.state += self.coeff * (x - self.state);
        self.state
    }
}

/// One channel's delay line: a circular buffer plus the feedback path's
/// lowpass state. `tick` both reads the current delayed sample (for the
/// wet output) and writes the next one (dry input plus the saturated,
/// lowpassed feedback) — see the module doc comment for why the value
/// read out already carries any accumulated character from earlier
/// passes, with no extra processing needed at read time.
struct DelayChannel {
    buffer: Vec<f32>,
    write_idx: usize,
    lowpass: OnePoleLowpass,
}

impl DelayChannel {
    fn new(max_samples: usize, sample_rate: f32) -> Self {
        Self {
            buffer: vec![0.0; max_samples.max(1)],
            write_idx: 0,
            lowpass: OnePoleLowpass::new(FEEDBACK_LOWPASS_HZ, sample_rate),
        }
    }

    #[inline]
    fn tick(&mut self, input: f32, delay_samples: usize, feedback: f32) -> f32 {
        let len = self.buffer.len();
        let delay_samples = delay_samples.clamp(1, len - 1);
        let read_idx = (self.write_idx + len - delay_samples) % len;
        let delayed = self.buffer[read_idx];

        let warmed = self.lowpass.tick(delayed);
        let saturated = diode_clip(warmed, FEEDBACK_DRIVE_GAIN) * FEEDBACK_MAKEUP;

        self.buffer[self.write_idx] = input + feedback * saturated;
        self.write_idx = (self.write_idx + 1) % len;

        delayed
    }
}

/// Analog-style delay pedal effect: two independent [`DelayChannel`]s
/// (L/R). `mix` 0.0 (the default) bypasses bit-exactly.
pub struct AnalogDelay {
    left: DelayChannel,
    right: DelayChannel,
    sample_rate: f32,
    time_ms: f32,
    feedback: f32,
    mix: f32,
}

impl AnalogDelay {
    pub fn new(sample_rate: f32) -> Self {
        let max_samples = (MAX_DELAY_MS / 1000.0 * sample_rate) as usize + 1;
        Self {
            left: DelayChannel::new(max_samples, sample_rate),
            right: DelayChannel::new(max_samples, sample_rate),
            sample_rate,
            time_ms: DEFAULT_TIME_MS,
            feedback: 0.0,
            mix: 0.0,
        }
    }

    pub fn set_time_ms(&mut self, ms: f32) {
        self.time_ms = ms.clamp(MIN_DELAY_MS, MAX_DELAY_MS);
    }

    #[allow(dead_code)]
    pub fn time_ms(&self) -> f32 {
        self.time_ms
    }

    pub fn set_feedback(&mut self, feedback: f32) {
        self.feedback = feedback.clamp(0.0, MAX_FEEDBACK);
    }

    #[allow(dead_code)]
    pub fn feedback(&self) -> f32 {
        self.feedback
    }

    pub fn set_mix(&mut self, mix: f32) {
        self.mix = mix.clamp(0.0, 1.0);
    }

    #[allow(dead_code)]
    pub fn mix(&self) -> f32 {
        self.mix
    }

    pub fn process(&mut self, samples: &mut [f32]) {
        if self.mix <= 0.0 {
            return;
        }
        let delay_samples = (self.time_ms / 1000.0 * self.sample_rate) as usize;
        let feedback = self.feedback;
        let mix = self.mix;
        for chunk in samples.chunks_exact_mut(2) {
            let dry_l = chunk[0];
            let dry_r = chunk[1];
            let wet_l = self.left.tick(dry_l, delay_samples, feedback);
            let wet_r = self.right.tick(dry_r, delay_samples, feedback);
            chunk[0] = dry_l + mix * (wet_l - dry_l);
            chunk[1] = dry_r + mix * (wet_r - dry_r);
        }
    }
}

impl Default for AnalogDelay {
    fn default() -> Self {
        Self::new(48_000.0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_new_no_panic() {
        let _ = AnalogDelay::new(48_000.0);
    }

    #[test]
    fn test_mix_zero_is_bit_transparent() {
        let mut delay = AnalogDelay::new(48_000.0);
        delay.set_feedback(0.8);
        delay.set_time_ms(200.0);
        let mut samples = [0.0; 480];
        for (i, s) in samples.iter_mut().enumerate() {
            *s = (2.0 * std::f32::consts::PI * 440.0 * i as f32 / 48000.0).sin() * 0.3;
        }
        let before = samples;
        delay.process(&mut samples);
        assert_eq!(before, samples);
    }

    #[test]
    fn test_silence_in_silence_out() {
        let mut delay = AnalogDelay::new(48_000.0);
        delay.set_mix(1.0);
        delay.set_feedback(0.8);
        let mut samples = vec![0.0f32; 48_000 * 2]; // 1s, longer than max delay
        delay.process(&mut samples);
        for &s in &samples {
            assert!(s.abs() < 1e-6);
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_clamps() {
        let mut delay = AnalogDelay::new(48_000.0);
        delay.set_mix(-1.0);
        assert_eq!(delay.mix(), 0.0);
        delay.set_mix(2.0);
        assert_eq!(delay.mix(), 1.0);
        delay.set_feedback(-1.0);
        assert_eq!(delay.feedback(), 0.0);
        delay.set_feedback(2.0);
        assert_eq!(delay.feedback(), MAX_FEEDBACK);
        delay.set_time_ms(-5.0);
        assert_eq!(delay.time_ms(), MIN_DELAY_MS);
        delay.set_time_ms(1.0e6);
        assert_eq!(delay.time_ms(), MAX_DELAY_MS);
    }

    /// A single impulse produces an audible echo at roughly the set
    /// delay time. Proves the delay line is actually delaying, not just
    /// passing silence or acting as a straight mixer.
    #[test]
    fn test_impulse_produces_delayed_echo() {
        let mut delay = AnalogDelay::new(48_000.0);
        delay.set_mix(1.0);
        delay.set_feedback(0.0); // single echo, no repeats
        delay.set_time_ms(100.0); // 4800 samples at 48kHz

        let n = 48_000 / 2; // 0.5s, comfortably past the echo
        let mut samples = vec![0.0f32; n * 2];
        samples[0] = 1.0;
        samples[1] = 1.0;
        delay.process(&mut samples);

        // Nothing before the echo arrives (a little slack for the
        // one-pole filter's settling).
        for frame in 0..4700 {
            assert!(
                samples[frame * 2].abs() < 1e-3,
                "unexpected energy at frame {frame} before the echo: {}",
                samples[frame * 2]
            );
        }
        // Some energy shows up around the expected delay time.
        let echo_energy: f32 = samples[4700 * 2..4900 * 2]
            .iter()
            .map(|s| s.abs())
            .fold(0.0, f32::max);
        assert!(
            echo_energy > 1e-3,
            "expected an echo around 100ms, got peak {echo_energy}"
        );
    }

    #[test]
    fn test_no_nan_or_inf_at_max_feedback() {
        let mut delay = AnalogDelay::new(48_000.0);
        delay.set_mix(1.0);
        delay.set_feedback(1.0); // clamps to MAX_FEEDBACK
        delay.set_time_ms(50.0);
        let mut samples = vec![0.0f32; 48_000 * 4]; // 4s of feedback accumulation
        for i in 0..200 {
            samples[i * 2] = 1.0;
            samples[i * 2 + 1] = 1.0;
        }
        delay.process(&mut samples);
        for &s in &samples {
            assert!(s.is_finite(), "non-finite sample at max feedback");
        }
    }

    /// The load-bearing stability claim in the module doc comment,
    /// checked directly rather than just argued: even at max feedback,
    /// repeats settle into a bounded range rather than growing without
    /// limit render-over-render. Compares the peak amplitude of an
    /// early window of repeats against a much later window.
    #[test]
    fn analog_delay_repeats_stay_bounded() {
        let mut delay = AnalogDelay::new(48_000.0);
        delay.set_mix(1.0);
        delay.set_feedback(MAX_FEEDBACK);
        delay.set_time_ms(50.0); // short delay -> many repeats in a few seconds

        let seconds = 6;
        let mut samples = vec![0.0f32; 48_000 * seconds * 2];
        // A short burst to kick off the feedback loop.
        for i in 0..500 {
            samples[i * 2] = 0.5;
            samples[i * 2 + 1] = 0.5;
        }
        delay.process(&mut samples);

        let peak_in = |range: std::ops::Range<usize>| -> f32 {
            samples[range].iter().map(|s| s.abs()).fold(0.0, f32::max)
        };
        let early_peak = peak_in(0..48_000); // first second
        let late_peak = peak_in(48_000 * (seconds - 1) * 2..48_000 * seconds * 2); // last second

        assert!(early_peak.is_finite() && late_peak.is_finite());
        // Generous bound — this isn't pinning an exact steady-state
        // level, just that repeats settle rather than diverge.
        assert!(
            late_peak <= early_peak * 3.0 + 0.1,
            "repeats appear to be growing without bound: early={early_peak}, late={late_peak}"
        );
    }
}
