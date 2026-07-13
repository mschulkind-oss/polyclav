//! Classic optical/bias-style amplitude-modulation tremolo (Fender
//! blackface "vibrato" channel, most stompbox tremolos) — a sine LFO
//! directly scales the signal's amplitude. Unlike the other three
//! pedals in this crate, there is no `mix` knob: the whole effect *is*
//! an amplitude envelope, so `depth` alone already spans "no effect"
//! (0.0) to "full chop to silence" (1.0). This mirrors the native
//! synth's own LFO `to_amp` (tremolo) destination
//! (`synth::NativeSynth`'s `lfo_to_amp`, ROADMAP §1.1 "GLOBAL" block) —
//! same two-parameter shape, same reason: `gain(t) = 1 - depth *
//! (lfo(t) * 0.5 + 0.5)` generalized from "one voice's LFO tap" to "the
//! whole post-synth signal, any backend."
//!
//! Mono by design: both channels are scaled by the same LFO phase (no
//! stereo offset, unlike [`crate::dsp::Chorus`]) — true to how nearly
//! every real tremolo pedal works. Because `gain(t)` is a pure
//! attenuation curve confined to `[1 - depth, 1]`, this stage can never
//! make a sample louder than its input — see
//! `tests::test_never_louder_than_dry`.
//!
//! See docs/VISION.md §1d for the full design writeup and chain-order
//! rationale (sits with [`crate::dsp::Chorus`] in the "modulation" slot,
//! before the delay).

const MIN_RATE_HZ: f32 = 0.05;
const MAX_RATE_HZ: f32 = 20.0;
const DEFAULT_RATE_HZ: f32 = 4.0;

/// Optical/bias tremolo. `depth` 0.0 (the default) bypasses bit-exactly
/// — no separate `mix` knob (see module doc comment).
pub struct Tremolo {
    sample_rate: f32,
    phase: f32,
    rate_hz: f32,
    depth: f32,
}

impl Tremolo {
    pub fn new(sample_rate: f32) -> Self {
        Self {
            sample_rate,
            phase: 0.0,
            rate_hz: DEFAULT_RATE_HZ,
            depth: 0.0,
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

    pub fn process(&mut self, samples: &mut [f32]) {
        if self.depth <= 0.0 {
            return;
        }
        let depth = self.depth;
        let phase_inc = self.rate_hz / self.sample_rate;

        for chunk in samples.chunks_exact_mut(2) {
            let lfo = (2.0 * std::f32::consts::PI * self.phase).sin();
            let gain = 1.0 - depth * (lfo * 0.5 + 0.5);
            chunk[0] *= gain;
            chunk[1] *= gain;

            self.phase += phase_inc;
            if self.phase >= 1.0 {
                self.phase -= 1.0;
            }
        }
    }
}

impl Default for Tremolo {
    fn default() -> Self {
        Self::new(48_000.0)
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
        let _ = Tremolo::new(48_000.0);
    }

    #[test]
    fn test_depth_zero_is_bit_transparent() {
        let mut tremolo = Tremolo::new(48_000.0);
        tremolo.set_rate_hz(8.0);
        let mut samples = to_stereo(&sine(0.5, 440.0, 480));
        let before = samples.clone();
        tremolo.process(&mut samples);
        assert_eq!(before, samples);
    }

    #[test]
    fn test_silence_in_silence_out() {
        let mut tremolo = Tremolo::new(48_000.0);
        tremolo.set_depth(1.0);
        let mut samples = vec![0.0f32; 4800];
        tremolo.process(&mut samples);
        for &s in &samples {
            assert!(s.abs() < 1e-6);
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_no_nan_or_inf_on_loud_signal() {
        let mut tremolo = Tremolo::new(48_000.0);
        tremolo.set_depth(1.0);
        tremolo.set_rate_hz(20.0);
        let mut samples = vec![0.0f32; 4096];
        for (i, s) in samples.iter_mut().enumerate() {
            *s = if i % 2 == 0 { 1.0 } else { -1.0 };
        }
        tremolo.process(&mut samples);
        for &s in &samples {
            assert!(s.is_finite());
        }
    }

    #[test]
    fn test_clamps() {
        let mut tremolo = Tremolo::new(48_000.0);
        tremolo.set_depth(-1.0);
        assert_eq!(tremolo.depth(), 0.0);
        tremolo.set_depth(2.0);
        assert_eq!(tremolo.depth(), 1.0);
        tremolo.set_rate_hz(-5.0);
        assert_eq!(tremolo.rate_hz(), MIN_RATE_HZ);
        tremolo.set_rate_hz(1.0e6);
        assert_eq!(tremolo.rate_hz(), MAX_RATE_HZ);
    }

    #[test]
    fn test_full_depth_changes_signal() {
        let mut tremolo = Tremolo::new(48_000.0);
        tremolo.set_depth(1.0);
        tremolo.set_rate_hz(4.0);
        let input = to_stereo(&sine(0.5, 440.0, 4800));
        let mut output = input.clone();
        tremolo.process(&mut output);
        let diff_sq: f32 = input
            .iter()
            .zip(output.iter())
            .map(|(a, b)| (a - b).powi(2))
            .sum();
        assert!(diff_sq > 0.0, "tremolo should modify the signal");
    }

    /// The core correctness invariant this pedal can assert exactly
    /// (unlike chorus/delay, where wet/dry mixing can produce
    /// constructive overshoot): pure attenuation means the output can
    /// never exceed the input in magnitude, at any depth or rate.
    #[test]
    fn test_never_louder_than_dry() {
        let mut tremolo = Tremolo::new(48_000.0);
        tremolo.set_depth(1.0);
        tremolo.set_rate_hz(6.0);
        let input = to_stereo(&sine(0.8, 440.0, 9600));
        let mut output = input.clone();
        tremolo.process(&mut output);
        for (dry, wet) in input.iter().zip(output.iter()) {
            assert!(
                wet.abs() <= dry.abs() + 1e-6,
                "tremolo output {wet} exceeded dry input {dry} in magnitude"
            );
        }
    }

    /// At full depth the envelope must actually swing from silence (the
    /// LFO trough) to near-full amplitude (the LFO peak), not just
    /// dip a little — otherwise "full chop to silence" isn't real. Uses
    /// a constant (non-oscillating) input so the swing measured is
    /// purely the tremolo's own gain envelope, not confounded by a sine
    /// input's own zero crossings.
    #[test]
    fn test_full_depth_swings_from_silence_to_full_amplitude() {
        let mut tremolo = Tremolo::new(48_000.0);
        tremolo.set_depth(1.0);
        tremolo.set_rate_hz(2.0); // one full cycle every 24000 frames
        let n = 48_000usize; // two full cycles
        let const_val = 0.8f32;
        let mut samples = vec![const_val; n * 2];
        tremolo.process(&mut samples);

        let min_abs = samples.iter().map(|s| s.abs()).fold(f32::MAX, f32::min);
        let max_abs = samples.iter().map(|s| s.abs()).fold(0.0f32, f32::max);
        assert!(
            min_abs < 0.02,
            "expected the envelope to reach near-silence at the LFO trough, got min |s|={min_abs}"
        );
        assert!(
            max_abs > const_val * 0.95,
            "expected the envelope to reach near-full amplitude at the LFO peak, got max |s|={max_abs}"
        );
    }
}
