//! One-pole parameter smoother for amplitude-multiplicative knobs.
//!
//! ## Scope (documented decision — the deliberate ROADMAP §1.6 subset)
//!
//! Per-block atomic pushes step parameters once per ~2.7 ms block, which
//! zippers audibly under live knob sweeps on any parameter that
//! MULTIPLIES the signal. This smoother is applied per-sample to exactly
//! those knobs where the zipper is most audible:
//!
//! - the three oscillator mixer levels,
//! - the noise mixer level,
//! - the drive pre-gain (the normalization is derived from the smoothed
//!   gain each time it moves),
//! - the pulse width,
//! - the global LFO → amp (tremolo) depth.
//!
//! Cutoff and resonance deliberately stay block-stepped: retuning the
//! Moog ladder per sample costs a coefficient recompute per voice per
//! sample, and the voice already applies a >0.5 Hz retune hysteresis on
//! the composed effective cutoff (see `Voice::tick`) — smoothing the
//! knob underneath that hysteresis would buy little and fight it. This
//! is the documented deviation from §1.6's blanket "slew on modulation
//! sources".
//!
//! ## Transparency guarantees
//!
//! - **Steady state is bit-exact**: once `value == target`, `tick`
//!   returns the target with no arithmetic applied, so a never-moved
//!   knob renders bit-identically to the pre-smoother engine (the
//!   default-render goldens pin this).
//! - **Convergence terminates**: each tick that still differs from the
//!   target snaps to it once within [`SNAP_EPSILON`] (~ -80 dBFS for a
//!   unit-range level), so a moved knob reaches its target *exactly*
//!   instead of orbiting it in float dust.
//! - **Note starts are exact**: a voice firing from silence snaps its
//!   smoothers to their targets (`Voice::note_on`) — parameters set
//!   while nothing sounds take full effect from the first sample.

/// Time constant of the amplitude-parameter smoothers, in seconds.
/// ~2 ms: fast enough to feel immediate under a knob sweep, slow enough
/// to spread a full-range step over ~100 samples at 48 kHz (no zipper).
pub const AMP_SMOOTH_TAU_S: f32 = 0.002;

/// Absolute snap distance: once |value - target| falls below this, the
/// smoother lands on the target exactly. 1e-4 of a unit-range level is
/// a -80 dBFS step — inaudible — and it bounds the smoothing tail (and
/// any derived per-sample recompute, e.g. the drive norm's tanh) to
/// ~10 time constants after a knob move.
const SNAP_EPSILON: f32 = 1.0e-4;

/// A one-pole (exponential) parameter smoother:
/// `value += (target - value) * coeff` per sample, with an exact snap
/// onto the target once within [`SNAP_EPSILON`]. See the module docs
/// for scope and transparency guarantees.
#[derive(Clone, Copy, Debug)]
pub struct Smoothed {
    value: f32,
    target: f32,
    coeff: f32,
}

impl Smoothed {
    /// Build a smoother resting exactly at `value` (converged — ticking
    /// it returns `value` bit-exactly until the target moves) with the
    /// [`AMP_SMOOTH_TAU_S`] time constant at `sample_rate`.
    pub fn resting_at(value: f32, sample_rate: f32) -> Self {
        Self {
            value,
            target: value,
            coeff: Self::coeff_for_tau(AMP_SMOOTH_TAU_S, sample_rate),
        }
    }

    /// One-pole coefficient `1 - exp(-1/(tau * sr))`, evaluated in f64
    /// (mirrors the glide coefficient — `1 - exp(-x)` for small x loses
    /// precision in f32).
    fn coeff_for_tau(tau_s: f32, sample_rate: f32) -> f32 {
        (-(-1.0f64 / (f64::from(tau_s) * f64::from(sample_rate))).exp_m1()) as f32
    }

    /// Retarget the smoother. The value slews toward the new target on
    /// subsequent `tick`s; setting the current target again is free.
    pub fn set_target(&mut self, target: f32) {
        self.target = target;
    }

    /// Jump to the target immediately (used when a voice fires from
    /// silence so note starts are exact — see the module docs).
    pub fn snap_to_target(&mut self) {
        self.value = self.target;
    }

    /// Current smoothed value without advancing.
    pub fn get(&self) -> f32 {
        self.value
    }

    /// Current target (test probe — production code only retargets and
    /// ticks).
    #[cfg(test)]
    pub fn target(&self) -> f32 {
        self.target
    }

    /// `true` once the value sits exactly on the target (steady state —
    /// `tick` is then a pure read).
    pub fn converged(&self) -> bool {
        self.value == self.target
    }

    /// Advance one sample and return the smoothed value. Bit-exact
    /// pass-through in steady state; snaps onto the target once within
    /// [`SNAP_EPSILON`].
    #[inline]
    pub fn tick(&mut self) -> f32 {
        if self.value != self.target {
            self.value += (self.target - self.value) * self.coeff;
            if (self.value - self.target).abs() < SNAP_EPSILON {
                self.value = self.target;
            }
        }
        self.value
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Steady state is a bit-exact pass-through: no arithmetic touches
    /// the value while it equals the target.
    #[test]
    fn steady_state_is_bit_exact() {
        let mut s = Smoothed::resting_at(0.7, 48_000.0);
        for _ in 0..1000 {
            assert_eq!(s.tick().to_bits(), 0.7f32.to_bits());
        }
        assert!(s.converged());
    }

    /// A retargeted smoother moves monotonically, lands within 1% of
    /// the step after 10 ms (5 time constants), and converges EXACTLY
    /// (snap) shortly after.
    #[test]
    fn step_converges_to_exact_target() {
        let mut s = Smoothed::resting_at(1.0, 48_000.0);
        s.set_target(0.25);
        let mut prev = s.get();
        for _ in 0..480 {
            // 10 ms
            let v = s.tick();
            assert!(v <= prev, "one-pole step must be monotonic");
            prev = v;
        }
        assert!(
            (s.get() - 0.25).abs() < 0.01 * 0.75,
            "must land within 1% of the step after 10 ms, got {}",
            s.get()
        );
        for _ in 0..4800 {
            s.tick();
        }
        assert_eq!(
            s.get().to_bits(),
            0.25f32.to_bits(),
            "smoother must snap exactly onto the target"
        );
        assert!(s.converged());
    }

    /// `snap_to_target` lands exactly and immediately.
    #[test]
    fn snap_is_exact() {
        let mut s = Smoothed::resting_at(0.0, 48_000.0);
        s.set_target(1.0);
        s.tick();
        assert!(!s.converged());
        s.snap_to_target();
        assert!(s.converged());
        assert_eq!(s.get().to_bits(), 1.0f32.to_bits());
    }
}
