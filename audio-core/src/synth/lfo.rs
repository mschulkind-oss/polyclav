//! Global low-frequency oscillator (ROADMAP §1.1 GLOBAL block).
//!
//! One `Lfo` is shared across all voices (owned by `NativeSynth`) and
//! advanced exactly once per output sample in `NativeSynth::render`. It
//! free-runs even while every destination depth is 0, so engaging a
//! depth mid-performance doesn't jump the phase. Output is bipolar in
//! [-1, 1] for every waveform.
//!
//! Waveforms (the `u32` wire encoding used by the DSP atomics and the
//! C ABI is 0/1/2/3):
//!
//! - `Triangle` (0): starts at 0, peaks +1 at ¼ cycle, -1 at ¾ cycle —
//!   the classic vibrato shape.
//! - `Saw` (1): rising ramp -1 → +1 per cycle.
//! - `Square` (2): +1 for the first half cycle, -1 for the second.
//! - `SampleHold` (3): a deterministic xorshift32 sampled once per
//!   cycle (stepped exactly when the phase wraps), held constant in
//!   between. The seed is fixed, so two identically-driven synths
//!   produce bit-identical S&H sequences — renders stay reproducible
//!   in tests.
//!
//! Tempo-sync (Play-as-tap-tempo, §2.5) is a later phase; this stage
//! ships the free-running rate knob only.

/// Selectable LFO waveform. The `u32` encoding (0/1/2/3) is the wire
/// format used by the DSP atomics and the C ABI.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum LfoWave {
    #[default]
    Triangle,
    Saw,
    Square,
    SampleHold,
}

impl LfoWave {
    /// Decode the atomic/FFI encoding (0 = triangle, 1 = saw,
    /// 2 = square, 3 = sample-and-hold).
    pub fn from_u32(v: u32) -> Option<Self> {
        match v {
            0 => Some(LfoWave::Triangle),
            1 => Some(LfoWave::Saw),
            2 => Some(LfoWave::Square),
            3 => Some(LfoWave::SampleHold),
            _ => None,
        }
    }
}

/// Rate clamp bounds in Hz (shared by the synth setter, the DSP atomic
/// clamp in `lib.rs`, and the default-rate documentation).
pub const MIN_RATE_HZ: f32 = 0.05;
pub const MAX_RATE_HZ: f32 = 20.0;
/// Default rate: 5 Hz — classic vibrato territory.
pub const DEFAULT_RATE_HZ: f32 = 5.0;

/// The single global LFO. See the module docs for the model.
pub struct Lfo {
    sample_rate: f32,
    wave: LfoWave,
    /// Rate in Hz, clamped to [`MIN_RATE_HZ`, `MAX_RATE_HZ`].
    rate_hz: f32,
    /// Cached per-sample phase increment `rate_hz / sample_rate`,
    /// recomputed only when the rate changes.
    phase_inc: f32,
    /// Phase in [0, 1). The output is computed from the CURRENT phase,
    /// then the phase advances — so the first sample of a fresh LFO
    /// reads the waveform at phase 0.
    phase: f32,
    /// xorshift32 state for the S&H waveform. Fixed nonzero seed —
    /// deterministic across identically-driven instances.
    sh_state: u32,
    /// Current S&H output, resampled from the xorshift each time the
    /// phase wraps (once per cycle).
    sh_value: f32,
}

impl Lfo {
    /// Build an LFO at the given sample rate with the §1.4-flavored
    /// defaults (triangle, [`DEFAULT_RATE_HZ`]).
    pub fn new(sample_rate: f32) -> Self {
        let mut lfo = Self {
            sample_rate,
            wave: LfoWave::Triangle,
            rate_hz: DEFAULT_RATE_HZ,
            phase_inc: DEFAULT_RATE_HZ / sample_rate,
            phase: 0.0,
            // Any nonzero seed works for xorshift32; distinct from the
            // voice NoiseGen seed so the two streams never correlate.
            sh_state: 0x9E37_79B9,
            sh_value: 0.0,
        };
        // Prime the S&H output so the first cycle already holds a
        // random value instead of a hardcoded 0.
        lfo.sh_value = lfo.sh_step();
        lfo
    }

    /// Set the waveform. Switching keeps the running phase (and the
    /// held S&H value), mirroring the oscillators' behavior.
    pub fn set_wave(&mut self, wave: LfoWave) {
        self.wave = wave;
    }

    /// Set the rate in Hz, clamped to [`MIN_RATE_HZ`, `MAX_RATE_HZ`].
    /// Change-detected so the divide only runs when the knob moves;
    /// the phase is preserved across rate changes.
    pub fn set_rate_hz(&mut self, rate_hz: f32) {
        let rate_hz = rate_hz.clamp(MIN_RATE_HZ, MAX_RATE_HZ);
        if rate_hz != self.rate_hz {
            self.rate_hz = rate_hz;
            self.phase_inc = rate_hz / self.sample_rate;
        }
    }

    /// One xorshift32 step mapped to a uniform value in [-1, 1].
    fn sh_step(&mut self) -> f32 {
        let mut x = self.sh_state;
        x ^= x << 13;
        x ^= x >> 17;
        x ^= x << 5;
        self.sh_state = x;
        (x as f32) * (2.0 / u32::MAX as f32) - 1.0
    }

    /// Render one bipolar LFO sample and advance the phase. The S&H
    /// generator steps exactly once per completed cycle (on phase
    /// wrap), regardless of the selected waveform — so switching to
    /// S&H mid-performance lands on the same deterministic sequence.
    pub fn tick(&mut self) -> f32 {
        let p = self.phase;
        let out = match self.wave {
            LfoWave::Triangle => {
                if p < 0.25 {
                    4.0 * p
                } else if p < 0.75 {
                    2.0 - 4.0 * p
                } else {
                    4.0 * p - 4.0
                }
            }
            LfoWave::Saw => 2.0 * p - 1.0,
            LfoWave::Square => {
                if p < 0.5 {
                    1.0
                } else {
                    -1.0
                }
            }
            LfoWave::SampleHold => self.sh_value,
        };
        self.phase += self.phase_inc;
        if self.phase >= 1.0 {
            self.phase -= 1.0;
            self.sh_value = self.sh_step();
        }
        out
    }

    /// Current rate in Hz (test probe for the clamp behavior).
    #[cfg(test)]
    pub(crate) fn rate_hz(&self) -> f32 {
        self.rate_hz
    }

    /// Current waveform (test probe for the wave fallback behavior).
    #[cfg(test)]
    pub(crate) fn wave(&self) -> LfoWave {
        self.wave
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Render `n` samples from a fresh LFO at `rate_hz`.
    fn run(wave: LfoWave, rate_hz: f32, n: usize) -> Vec<f32> {
        let mut lfo = Lfo::new(48_000.0);
        lfo.set_wave(wave);
        lfo.set_rate_hz(rate_hz);
        (0..n).map(|_| lfo.tick()).collect()
    }

    /// Every waveform stays within the bipolar range and actually
    /// spans (most of) it over a few cycles.
    #[test]
    fn waveforms_are_bipolar() {
        for wave in [
            LfoWave::Triangle,
            LfoWave::Saw,
            LfoWave::Square,
            LfoWave::SampleHold,
        ] {
            // 10 cycles of 10 Hz at 48 kHz.
            let out = run(wave, 10.0, 48_000);
            let min = out.iter().fold(f32::INFINITY, |m, &v| m.min(v));
            let max = out.iter().fold(f32::NEG_INFINITY, |m, &v| m.max(v));
            assert!(
                (-1.0..=1.0).contains(&min) && (-1.0..=1.0).contains(&max),
                "{wave:?} out of range: min={min} max={max}"
            );
            assert!(
                max - min > 0.5,
                "{wave:?} should span the bipolar range, got min={min} max={max}"
            );
        }
    }

    /// The triangle starts at 0, peaks +1 at ¼ cycle, and bottoms out
    /// -1 at ¾ cycle (10 Hz at 48 kHz ⇒ 4800-sample cycle).
    #[test]
    fn triangle_shape_and_period() {
        let out = run(LfoWave::Triangle, 10.0, 9_600);
        assert_eq!(out[0], 0.0, "triangle starts at 0");
        assert!((out[1200] - 1.0).abs() < 1e-3, "peak at ¼ cycle");
        assert!((out[3600] + 1.0).abs() < 1e-3, "trough at ¾ cycle");
        assert!(out[4800].abs() < 1e-3, "back to 0 after one cycle");
        assert!((out[6000] - 1.0).abs() < 1e-3, "second-cycle peak");
    }

    /// The S&H output holds a constant value within each cycle and
    /// steps to a new one when the phase wraps: over 4 cycles the
    /// output is exactly 4 constant runs, each one cycle (± a sample of
    /// float phase-accumulation slack) long.
    #[test]
    fn sample_hold_steps_once_per_cycle() {
        let out = run(LfoWave::SampleHold, 10.0, 4 * 4_800);
        let mut runs: Vec<(f32, usize)> = Vec::new();
        for &v in &out {
            match runs.last_mut() {
                Some((held, len)) if *held == v => *len += 1,
                _ => runs.push((v, 1)),
            }
        }
        assert_eq!(runs.len(), 4, "expected 4 held values over 4 cycles");
        for (i, &(_, len)) in runs.iter().enumerate().take(3) {
            assert!(
                (4_799..=4_801).contains(&len),
                "run {i} should last one 4800-sample cycle, got {len}"
            );
        }
        for w in runs.windows(2) {
            assert_ne!(w[0].0, w[1].0, "consecutive cycles should differ");
        }
    }

    /// Two identically-driven LFOs produce bit-identical S&H streams
    /// (fixed seed — deterministic renders).
    #[test]
    fn sample_hold_is_deterministic() {
        let a = run(LfoWave::SampleHold, 7.0, 20_000);
        let b = run(LfoWave::SampleHold, 7.0, 20_000);
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(x.to_bits(), y.to_bits(), "sample {i} diverged: {x} vs {y}");
        }
    }

    /// `set_rate_hz` clamps to [0.05, 20] Hz.
    #[test]
    fn set_rate_clamps() {
        let mut lfo = Lfo::new(48_000.0);
        lfo.set_rate_hz(100.0);
        assert_eq!(lfo.rate_hz(), 20.0);
        lfo.set_rate_hz(0.0);
        assert_eq!(lfo.rate_hz(), 0.05);
        lfo.set_rate_hz(5.0);
        assert_eq!(lfo.rate_hz(), 5.0);
    }

    /// The wire encoding round-trips and rejects garbage.
    #[test]
    fn lfo_wave_from_u32() {
        assert_eq!(LfoWave::from_u32(0), Some(LfoWave::Triangle));
        assert_eq!(LfoWave::from_u32(1), Some(LfoWave::Saw));
        assert_eq!(LfoWave::from_u32(2), Some(LfoWave::Square));
        assert_eq!(LfoWave::from_u32(3), Some(LfoWave::SampleHold));
        assert_eq!(LfoWave::from_u32(4), None);
    }
}
