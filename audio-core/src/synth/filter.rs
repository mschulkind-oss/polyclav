//! Per-voice resonant lowpass.
//!
//! Phase 1 wraps `fundsp::Moog` in its `U1` variant — cutoff and Q are
//! set via `set_cutoff_q`, audio rate input is the only port. fundsp's
//! ladder is the Stilson/Smith musicdsp variant with a tanh saturator on
//! the fourth stage only (NOT Huovilainen — no per-stage tanh, no
//! thermal-voltage scaling; see docs/ROADMAP.md Appendix A). Resonance
//! still behaves musically; we tune Q a bit below self-oscillation by
//! default (see `synth::voice::DEFAULT_RESONANCE`).
//!
//! Phase 2 may switch to the `U3` variant if we want sample-rate cutoff
//! modulation; for Phase 1, "set cutoff once per audio block" is
//! sufficient and avoids the per-sample setting overhead.

use fundsp::audionode::AudioNode;
use fundsp::moog::Moog;
use fundsp::typenum::U1;

/// 24 dB/oct resonant lowpass, Moog-style ladder (Stilson/Smith variant).
pub struct MoogFilter {
    inner: Moog<f32, U1>,
}

impl MoogFilter {
    /// Construct a filter at the given sample rate, cutoff (Hz), and Q
    /// (0..~1; self-oscillation onset ≈ 1.0).
    pub fn new(sample_rate: f32, cutoff_hz: f32, q: f32) -> Self {
        let mut inner = Moog::<f32, U1>::new(cutoff_hz, q);
        inner.set_sample_rate(sample_rate as f64);
        // set_sample_rate retunes via the stored cutoff/q; re-set
        // explicitly so the coefficients reflect the requested sample rate.
        inner.set_cutoff_q(cutoff_hz, q);
        Self { inner }
    }

    /// Retune the filter without resetting its internal state. Safe to
    /// call every block (or every sample if needed).
    pub fn set_cutoff_q(&mut self, cutoff_hz: f32, q: f32) {
        self.inner.set_cutoff_q(cutoff_hz, q);
    }

    /// Process one sample.
    pub fn tick(&mut self, sample: f32) -> f32 {
        let out = self.inner.tick(&[sample].into());
        out[0]
    }
}
