//! BBD-style analog chorus (Boss CE-2 / Small Clone territory): a short,
//! LFO-modulated delay line mixed with the dry signal. The modulation
//! *is* the effect — the LFO continuously sweeps the delay tap's read
//! position, and the resulting Doppler shift on the delayed copy is what
//! reads as "chorus" rather than "echo."
//!
//! **Why this isn't just [`crate::dsp::AnalogDelay`] with a small,
//! wiggling `time_ms`:** `AnalogDelay`'s delay line reads at integer
//! sample offsets only — fine for a delay whose length changes rarely
//! (a knob turn), but a chorus sweeps its tap position continuously and
//! fast enough that integer-only reads step audibly (zipper noise)
//! instead of gliding. This module's delay line reads with linear
//! interpolation between adjacent samples instead, so the tap can move
//! smoothly at audio rate.
//!
//! **Stereo width for free:** the two channels run independent delay
//! lines sharing one LFO rate but offset 90° in phase — a standard
//! analog-chorus trick (rack/stereo units like the Dimension D do this;
//! a mono BBD pedal like the CE-2 doesn't) that widens the effect
//! without a fourth knob.
//!
//! **Warmth:** the wet tap gets a gentle one-pole lowpass (bandwidth
//! loss) — the same "this passed through a bucket-brigade chip's
//! anti-aliasing filter" idiom `AnalogDelay`'s feedback path uses.
//! Deliberately *not* also running through
//! [`crate::dsp::saturate::diode_clip`] the way `AnalogDelay`'s
//! feedback path does: that function's asinh model is calibrated to
//! always be deep in saturation for any audible input regardless of
//! how small a `pre_gain` it's given (see `dsp::drive_pedal`'s module
//! doc comment for the full explanation) — exactly right for a fully
//! crossfaded drive stage or a feedback loop that needs bounding, but
//! wrong for "a little texture on a single pass": it pegs the wet tap
//! to a near-constant, input-independent amplitude, which showed up
//! directly as a loudness-sweep failure (`chorus_mix_sweep_is_smooth`
//! in `lib.rs`) during development — a real, caught-not-theoretical
//! bug, the same kind of thing the drive pedal's own v1→v2 loudness fix
//! caught. The lowpass alone is genuinely BBD-flavored and behaves
//! linearly (no surprise loudness jumps).
//!
//! `rate_hz` and `depth` reshape the wet signal; `mix` (0 = bypass,
//! dry-only) is the sole bit-exact bypass gate, the same shape as every
//! other pedal in this crate. See docs/VISION.md §1c for the full
//! design writeup and chain-order rationale.

const DEFAULT_SAMPLE_RATE: f32 = 48_000.0;

/// Base (unmodulated) delay in ms — a classic short-chorus pre-delay,
/// distinct from vibrato (shorter, no dry mix) and flanger (shorter
/// still, with feedback).
const BASE_DELAY_MS: f32 = 7.0;
/// Maximum modulation depth in ms at `depth = 1.0` — total sweep range
/// is `BASE_DELAY_MS` ± this, i.e. 4–10 ms.
const MAX_DEPTH_MS: f32 = 3.0;

const MIN_RATE_HZ: f32 = 0.02;
const MAX_RATE_HZ: f32 = 5.0;
const DEFAULT_RATE_HZ: f32 = 0.8;

/// L/R LFO phase offset in cycles (90°) — the stereo-width trick.
const STEREO_PHASE_OFFSET_CYCLES: f32 = 0.25;

/// One-pole lowpass cutoff applied to the wet tap — gentler (brighter)
/// than `AnalogDelay::FEEDBACK_LOWPASS_HZ` (3.5 kHz) since this is a
/// single pass, not an accumulating feedback loop. See the module doc
/// comment for why this is the *only* warmth stage (no saturation).
const WARMTH_LOWPASS_HZ: f32 = 6_000.0;

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

/// One channel's modulated delay line: a circular buffer read with
/// linear interpolation at a continuously-moving fractional offset, plus
/// its own LFO phase (so L/R can run at a fixed phase offset) and warmth
/// filter state.
struct ChorusChannel {
    buffer: Vec<f32>,
    write_idx: usize,
    lfo_phase: f32,
    lowpass: OnePoleLowpass,
}

impl ChorusChannel {
    fn new(max_delay_samples: usize, sample_rate: f32, lfo_phase: f32) -> Self {
        Self {
            buffer: vec![0.0; max_delay_samples.max(4)],
            write_idx: 0,
            lfo_phase,
            lowpass: OnePoleLowpass::new(WARMTH_LOWPASS_HZ, sample_rate),
        }
    }

    /// Linear-interpolated read `delay_samples` behind the write head.
    #[inline]
    fn read_fractional(&self, delay_samples: f32) -> f32 {
        let len = self.buffer.len();
        // Leave one sample of headroom below `len` for the +1 tap.
        let delay_samples = delay_samples.clamp(0.0, (len - 2) as f32);
        let base = delay_samples.floor();
        let frac = delay_samples - base;
        let d0 = base as usize;
        let d1 = d0 + 1;
        let idx0 = (self.write_idx + len - d0) % len;
        let idx1 = (self.write_idx + len - d1) % len;
        self.buffer[idx0] * (1.0 - frac) + self.buffer[idx1] * frac
    }

    #[inline]
    fn tick(
        &mut self,
        input: f32,
        rate_hz: f32,
        base_delay_samples: f32,
        depth_samples: f32,
        sample_rate: f32,
    ) -> f32 {
        let lfo = (2.0 * std::f32::consts::PI * self.lfo_phase).sin();
        let delay_samples = base_delay_samples + depth_samples * lfo;

        let raw = self.read_fractional(delay_samples);
        let wet = self.lowpass.tick(raw);

        let len = self.buffer.len();
        self.buffer[self.write_idx] = input;
        self.write_idx = (self.write_idx + 1) % len;

        self.lfo_phase += rate_hz / sample_rate;
        if self.lfo_phase >= 1.0 {
            self.lfo_phase -= 1.0;
        }

        wet
    }
}

/// BBD-style stereo chorus. `mix` 0.0 (the default) bypasses bit-exactly.
pub struct Chorus {
    left: ChorusChannel,
    right: ChorusChannel,
    sample_rate: f32,
    rate_hz: f32,
    depth: f32,
    mix: f32,
}

impl Chorus {
    pub fn new(sample_rate: f32) -> Self {
        let max_delay_samples =
            ((BASE_DELAY_MS + MAX_DEPTH_MS) / 1000.0 * sample_rate) as usize + 4;
        Self {
            left: ChorusChannel::new(max_delay_samples, sample_rate, 0.0),
            right: ChorusChannel::new(max_delay_samples, sample_rate, STEREO_PHASE_OFFSET_CYCLES),
            sample_rate,
            rate_hz: DEFAULT_RATE_HZ,
            depth: 0.0,
            mix: 0.0,
        }
    }

    pub fn set_rate_hz(&mut self, hz: f32) {
        self.rate_hz = hz.clamp(MIN_RATE_HZ, MAX_RATE_HZ);
    }

    #[allow(dead_code)]
    pub fn rate_hz(&self) -> f32 {
        self.rate_hz
    }

    pub fn set_depth(&mut self, depth: f32) {
        self.depth = depth.clamp(0.0, 1.0);
    }

    #[allow(dead_code)]
    pub fn depth(&self) -> f32 {
        self.depth
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
        let base_delay_samples = BASE_DELAY_MS / 1000.0 * self.sample_rate;
        let depth_samples = self.depth * MAX_DEPTH_MS / 1000.0 * self.sample_rate;
        let rate_hz = self.rate_hz;
        let mix = self.mix;
        let sample_rate = self.sample_rate;

        for chunk in samples.chunks_exact_mut(2) {
            let dry_l = chunk[0];
            let dry_r = chunk[1];
            let wet_l = self.left.tick(
                dry_l,
                rate_hz,
                base_delay_samples,
                depth_samples,
                sample_rate,
            );
            let wet_r = self.right.tick(
                dry_r,
                rate_hz,
                base_delay_samples,
                depth_samples,
                sample_rate,
            );
            chunk[0] = dry_l + mix * (wet_l - dry_l);
            chunk[1] = dry_r + mix * (wet_r - dry_r);
        }
    }
}

impl Default for Chorus {
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

    /// Interleave a mono signal into stereo (identical L/R input — the
    /// stereo divergence tests below check that the *effect* introduces
    /// width, not that the input already had any).
    fn to_stereo(mono: &[f32]) -> Vec<f32> {
        let mut out = Vec::with_capacity(mono.len() * 2);
        for &s in mono {
            out.push(s);
            out.push(s);
        }
        out
    }

    #[test]
    fn test_new_no_panic() {
        let _ = Chorus::new(48_000.0);
    }

    #[test]
    fn test_mix_zero_is_bit_transparent() {
        let mut chorus = Chorus::new(48_000.0);
        chorus.set_rate_hz(2.0);
        chorus.set_depth(1.0);
        let mut samples = to_stereo(&sine(0.3, 440.0, 480));
        let before = samples.clone();
        chorus.process(&mut samples);
        assert_eq!(before, samples);
    }

    #[test]
    fn test_silence_in_silence_out() {
        let mut chorus = Chorus::new(48_000.0);
        chorus.set_mix(1.0);
        chorus.set_depth(1.0);
        let mut samples = vec![0.0f32; 48_000];
        chorus.process(&mut samples);
        for &s in &samples {
            assert!(s.abs() < 1e-6);
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_no_nan_or_inf_on_loud_signal() {
        let mut chorus = Chorus::new(48_000.0);
        chorus.set_mix(1.0);
        chorus.set_depth(1.0);
        chorus.set_rate_hz(5.0);
        let mut samples = vec![0.0f32; 8192];
        for (i, s) in samples.iter_mut().enumerate() {
            *s = if i % 2 == 0 { 1.0 } else { -1.0 };
        }
        chorus.process(&mut samples);
        for &s in &samples {
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_clamps() {
        let mut chorus = Chorus::new(48_000.0);
        chorus.set_mix(-1.0);
        assert_eq!(chorus.mix(), 0.0);
        chorus.set_mix(2.0);
        assert_eq!(chorus.mix(), 1.0);
        chorus.set_depth(-1.0);
        assert_eq!(chorus.depth(), 0.0);
        chorus.set_depth(2.0);
        assert_eq!(chorus.depth(), 1.0);
        chorus.set_rate_hz(-5.0);
        assert_eq!(chorus.rate_hz(), MIN_RATE_HZ);
        chorus.set_rate_hz(1.0e6);
        assert_eq!(chorus.rate_hz(), MAX_RATE_HZ);
    }

    #[test]
    fn test_full_wet_changes_signal() {
        let mut chorus = Chorus::new(48_000.0);
        chorus.set_mix(1.0);
        chorus.set_depth(0.7);
        chorus.set_rate_hz(1.0);
        let input = to_stereo(&sine(0.3, 440.0, 4800));
        let mut output = input.clone();
        chorus.process(&mut output);
        let diff_sq: f32 = input
            .iter()
            .zip(output.iter())
            .map(|(a, b)| (a - b).powi(2))
            .sum();
        assert!(diff_sq > 0.0, "chorus should modify the signal");
    }

    /// Pins the stereo-width trick: with the same input on both
    /// channels, the 90°-offset L/R LFOs must produce genuinely
    /// different left/right output once wet — otherwise the "stereo
    /// width for free" design point isn't actually doing anything.
    #[test]
    fn test_stereo_channels_diverge_when_wet() {
        let mut chorus = Chorus::new(48_000.0);
        chorus.set_mix(1.0);
        chorus.set_depth(1.0);
        chorus.set_rate_hz(2.0);
        let mut samples = to_stereo(&sine(0.3, 440.0, 4800));
        chorus.process(&mut samples);

        let diff_sq: f32 = samples.chunks_exact(2).map(|c| (c[0] - c[1]).powi(2)).sum();
        assert!(
            diff_sq > 0.0,
            "left/right channels should diverge with a phase-offset stereo chorus"
        );
    }

    #[test]
    fn test_zero_depth_is_a_fixed_delay_not_silence() {
        // depth=0 means no modulation, but the wet path is still a
        // (fixed, short) delay — mix>0 should still audibly differ from
        // dry even with no LFO sweep.
        let mut chorus = Chorus::new(48_000.0);
        chorus.set_mix(1.0);
        chorus.set_depth(0.0);
        let input = to_stereo(&sine(0.3, 440.0, 4800));
        let mut output = input.clone();
        chorus.process(&mut output);
        let diff_sq: f32 = input
            .iter()
            .zip(output.iter())
            .map(|(a, b)| (a - b).powi(2))
            .sum();
        assert!(diff_sq > 0.0);
    }
}
