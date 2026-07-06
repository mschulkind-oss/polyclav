//! Per-voice resonant lowpass (plus its 2× oversampled variant).
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
//!
//! ## 2× oversampled drive + ladder ([`OversampledDriveLadder`])
//!
//! ROADMAP §0.1 / §1.6 / Appendix A pivot item (a): the nonlinear
//! section (tanh drive + the ladder's stage-4 tanh) aliases when driven
//! hard at 44.1/48 kHz — harmonics the tanh generates above Nyquist
//! fold back into the audible band. Running just that section at 2× the
//! sample rate moves the fold point an octave up and lets a halfband
//! lowpass remove the junk before decimating back.
//!
//! **Implementation choice (documented decision):** we wrap fundsp's
//! `Oversampler` around a tiny bespoke `DriveLadder` AudioNode instead
//! of hand-rolling the zero-stuff + halfband FIR. Reasons, from reading
//! `fundsp-0.23.0/src/oversample.rs`:
//!
//! - `Oversampler::tick` is exactly shaped for our per-sample voice
//!   loop: one base-rate sample in → two inner ticks at 2× → one
//!   base-rate sample out. No block adapters needed.
//! - Its halfband is a proven 43-tap minimum-phase Kaiser design
//!   (80 dB stopband, cutoff 0.22 × the 2× rate ≈ 21 kHz at 96 kHz),
//!   applied polyphase on the way up (zero-stuffing is implicit in the
//!   even/odd tap split) and full-rate on the way down. Hand-rolling
//!   duplicates ~150 lines of easy-to-get-subtly-wrong coefficient +
//!   ring-buffer code for zero benefit.
//! - `Oversampler::new(sr, node)` calls `node.set_sample_rate(2 * sr)`,
//!   and `Moog::set_sample_rate` retunes from its stored cutoff/Q — the
//!   "construct/retune the ladder at sample_rate × 2" requirement is
//!   satisfied by construction, and every later `set_cutoff_q` on the
//!   inner node computes coefficients for the doubled rate.
//!
//! The cost of this choice is the ~40-line `DriveLadder` AudioNode impl
//! below (the drive must run *inside* the wrapper to happen at 2×), a
//! few samples of minimum-phase group delay, and ~-1.5 dB right at the
//! top of the audible band (~20 kHz) — inaudible, and only when the
//! oversampled path is enabled.

use fundsp::audionode::AudioNode;
use fundsp::moog::Moog;
use fundsp::oversample::Oversampler;
use fundsp::typenum::U1;
use fundsp::Frame;

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

    /// Zero the ladder's internal state (coefficients keep their
    /// tuning). Used when the voice swaps back from the oversampled
    /// path so stale pre-toggle state doesn't ring into the hand-off.
    pub fn reset(&mut self) {
        self.inner.reset();
    }

    /// Process one sample.
    pub fn tick(&mut self, sample: f32) -> f32 {
        let out = self.inner.tick(&[sample].into());
        out[0]
    }
}

/// The nonlinear section (pre-filter tanh drive + Moog ladder) as a
/// fundsp `AudioNode`, so `Oversampler` can run it at 2× the base
/// sample rate (see the module docs for why fundsp's wrapper was chosen
/// over a hand-rolled halfband). The drive model is identical to the
/// voice's inline stage: `y = tanh(x * gain) * norm` with
/// `gain = 1 + drive*4`, `norm = 1/gain`, bypassed while `gain <= 1`
/// (drive 0). Parameters are plain fields pushed through
/// [`OversampledDriveLadder`]'s setters — never through fundsp's
/// `Setting` machinery.
#[derive(Clone)]
struct DriveLadder {
    drive_gain: f32,
    drive_norm: f32,
    moog: Moog<f32, U1>,
}

impl AudioNode for DriveLadder {
    // fundsp node IDs only feed the `ping` hash for graph fingerprints;
    // any value distinct from the built-in nodes (small integers) works.
    const ID: u64 = 0x706F_6C79_636C_6176; // "polyclav"
    type Inputs = U1;
    type Outputs = U1;

    fn reset(&mut self) {
        self.moog.reset();
    }

    fn set_sample_rate(&mut self, sample_rate: f64) {
        // Retunes from the Moog's stored cutoff/Q — this is what makes
        // `Oversampler::new` construct the ladder for the doubled rate.
        self.moog.set_sample_rate(sample_rate);
    }

    #[inline]
    fn tick(&mut self, input: &Frame<f32, Self::Inputs>) -> Frame<f32, Self::Outputs> {
        let mut x = input[0];
        if self.drive_gain > 1.0 {
            x = (x * self.drive_gain).tanh() * self.drive_norm;
        }
        self.moog.tick(&[x].into())
    }
}

/// 2× oversampled drive + ladder: the optional per-voice path enabled
/// by the `native_oversample` atomic. Upsample → tanh drive at 2× →
/// ladder retuned for 2× → decimate, all inside fundsp's `Oversampler`
/// (halfband details in the module docs). The base-rate API mirrors
/// [`MoogFilter`] plus a drive push.
pub struct OversampledDriveLadder {
    inner: Oversampler<DriveLadder>,
}

impl OversampledDriveLadder {
    /// Construct at the given **base** sample rate; the enclosed drive +
    /// ladder run at `sample_rate * 2` (the `Oversampler` constructor
    /// pushes the doubled rate into the node, retuning the ladder).
    pub fn new(sample_rate: f32, cutoff_hz: f32, q: f32) -> Self {
        let node = DriveLadder {
            drive_gain: 1.0,
            drive_norm: 1.0,
            moog: Moog::<f32, U1>::new(cutoff_hz, q),
        };
        Self {
            inner: Oversampler::new(sample_rate as f64, node),
        }
    }

    /// Retune the inner ladder (coefficients computed for the doubled
    /// rate) without resetting its state. Same call surface and
    /// lifecycle as [`MoogFilter::set_cutoff_q`].
    pub fn set_cutoff_q(&mut self, cutoff_hz: f32, q: f32) {
        self.inner.node_mut().moog.set_cutoff_q(cutoff_hz, q);
    }

    /// Push the drive pre-gain and its cached reciprocal (the voice
    /// already computes both — see `Voice::set_drive`). `gain <= 1`
    /// (drive 0) bypasses the tanh stage inside the wrapper.
    pub fn set_drive_gain(&mut self, gain: f32, norm: f32) {
        let node = self.inner.node_mut();
        node.drive_gain = gain;
        node.drive_norm = norm;
    }

    /// Zero the halfband FIR history and the ladder state (tuning and
    /// drive are kept). Called when the voice toggles the oversampled
    /// path in, so it never replays stale pre-toggle audio.
    pub fn reset(&mut self) {
        self.inner.reset();
    }

    /// Process one **base-rate** sample (two inner ticks at 2× happen
    /// inside the wrapper).
    #[inline]
    pub fn tick(&mut self, sample: f32) -> f32 {
        let out = self.inner.tick(&[sample].into());
        out[0]
    }
}
