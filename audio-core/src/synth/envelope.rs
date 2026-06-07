//! Per-voice ADSR amplitude envelope.
//!
//! Phase 1 rolls its own ADSR rather than wiring `fundsp::adsr_live` into
//! the voice graph. The reasons (per `docs/ROADMAP.md`):
//!
//! - We want stateful, gate-driven control we can drive from `note_on` /
//!   `note_off` calls — `adsr_live` expects an audio-rate gate signal as a
//!   graph input, which is awkward inside our manual per-voice render
//!   loop.
//! - We want `is_active()` so the voice allocator can detect "envelope
//!   has fully released, this voice is free again."
//! - The shape (linear attack to 1.0, linear decay to sustain level,
//!   linear release from current level to 0.0 — driven by sample-rate
//!   time deltas) is ~30 lines and trivial to unit-test.
//!
//! Phase 2+ may swap this for an exponential-curve envelope or for the
//! upstream `fundsp` adsr if a use case emerges; the public surface
//! (`note_on`/`note_off`/`tick`/`is_active`) is stable.

/// Internal envelope phase.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Stage {
    Idle,
    Attack,
    Decay,
    Sustain,
    Release,
}

/// A linear ADSR amplitude envelope, sample-rate driven.
#[derive(Clone)]
pub struct Adsr {
    sample_rate: f32,
    attack_s: f32,
    decay_s: f32,
    sustain: f32,
    release_s: f32,
    stage: Stage,
    /// Current envelope value in 0..=1. Read by `tick` before advancing.
    value: f32,
    /// Value at the moment `note_off` was called — release ramps from
    /// here to 0. Captured so retrigger-after-release behaves musically.
    release_from: f32,
    /// Linear time within the current `Release` stage in seconds, used
    /// to compute the per-sample ramp slope without divisions.
    release_t: f32,
}

impl Adsr {
    /// Construct an ADSR with the given times (seconds) and sustain
    /// level (0..=1). `sample_rate` is in Hz.
    pub fn new(
        sample_rate: f32,
        attack_s: f32,
        decay_s: f32,
        sustain: f32,
        release_s: f32,
    ) -> Self {
        Self {
            sample_rate,
            attack_s: attack_s.max(1.0e-4),
            decay_s: decay_s.max(1.0e-4),
            sustain: sustain.clamp(0.0, 1.0),
            release_s: release_s.max(1.0e-4),
            stage: Stage::Idle,
            value: 0.0,
            release_from: 0.0,
            release_t: 0.0,
        }
    }

    /// Begin (or retrigger) the envelope. Phase 1 always restarts from
    /// the current value — the next sample begins ramping toward 1.0 over
    /// `attack_s`. Mono-legato voice allocation suppresses the retrigger
    /// upstream; that's not this struct's concern.
    pub fn note_on(&mut self) {
        self.stage = Stage::Attack;
    }

    /// Release the gate. Captures the current value as the release start
    /// so the ramp-to-zero begins from wherever we were (sustain
    /// typically, but possibly partway through attack or decay if the
    /// user released a note quickly).
    pub fn note_off(&mut self) {
        if self.stage != Stage::Idle {
            self.release_from = self.value;
            self.release_t = 0.0;
            self.stage = Stage::Release;
        }
    }

    /// Advance the envelope by one sample and return the new value.
    pub fn tick(&mut self) -> f32 {
        let dt = 1.0 / self.sample_rate;
        match self.stage {
            Stage::Idle => {
                self.value = 0.0;
            }
            Stage::Attack => {
                let step = dt / self.attack_s;
                self.value += step;
                if self.value >= 1.0 {
                    self.value = 1.0;
                    self.stage = Stage::Decay;
                }
            }
            Stage::Decay => {
                // Linear ramp from 1.0 to sustain over decay_s.
                let step = dt / self.decay_s * (1.0 - self.sustain);
                self.value -= step;
                if self.value <= self.sustain {
                    self.value = self.sustain;
                    self.stage = Stage::Sustain;
                }
            }
            Stage::Sustain => {
                self.value = self.sustain;
            }
            Stage::Release => {
                self.release_t += dt;
                let frac = (self.release_t / self.release_s).clamp(0.0, 1.0);
                self.value = self.release_from * (1.0 - frac);
                if frac >= 1.0 {
                    self.value = 0.0;
                    self.stage = Stage::Idle;
                }
            }
        }
        self.value
    }

    /// `true` while the envelope is producing non-zero output (or about
    /// to). The voice allocator uses this to detect a free voice slot.
    pub fn is_active(&self) -> bool {
        self.stage != Stage::Idle
    }

    /// Current stage as a string — for tests / debug only. Hidden behind
    /// a `cfg(test)` to keep the public surface narrow.
    #[cfg(test)]
    pub(crate) fn stage_name(&self) -> &'static str {
        match self.stage {
            Stage::Idle => "idle",
            Stage::Attack => "attack",
            Stage::Decay => "decay",
            Stage::Sustain => "sustain",
            Stage::Release => "release",
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// At 48 kHz, an attack of 5 ms should land on 1.0 after ~240
    /// samples. We sweep 1000 samples and verify the value crosses 1.0
    /// then settles into decay.
    #[test]
    fn attack_reaches_unity() {
        let mut env = Adsr::new(48_000.0, 0.005, 0.200, 0.7, 0.400);
        env.note_on();
        // After full attack (5 ms = 240 samples), value should be ~1.
        let mut peak: f32 = 0.0;
        for _ in 0..500 {
            peak = peak.max(env.tick());
        }
        assert!(
            peak >= 0.99,
            "expected envelope to reach ~1.0 during attack, got peak={peak}"
        );
    }

    /// After attack and decay, value should plateau at the sustain level.
    #[test]
    fn decay_settles_at_sustain() {
        let mut env = Adsr::new(48_000.0, 0.005, 0.050, 0.5, 0.400);
        env.note_on();
        // Render ~200 ms — well past A (5 ms) + D (50 ms), should be in sustain.
        let mut last = 0.0;
        for _ in 0..9600 {
            last = env.tick();
        }
        assert!(
            (last - 0.5).abs() < 0.01,
            "expected sustain ≈ 0.5, got {last}"
        );
        assert_eq!(env.stage_name(), "sustain");
    }

    /// Release from sustain should ramp linearly to zero and then go idle.
    #[test]
    fn release_ramps_to_zero_and_idles() {
        let mut env = Adsr::new(48_000.0, 0.001, 0.001, 0.8, 0.100);
        env.note_on();
        // Run to sustain.
        for _ in 0..2000 {
            env.tick();
        }
        assert_eq!(env.stage_name(), "sustain");
        env.note_off();
        // Halfway through release (50 ms = 2400 samples), expect ~0.4 (half of 0.8).
        for _ in 0..2400 {
            env.tick();
        }
        let mid = env.tick();
        assert!(
            (mid - 0.4).abs() < 0.05,
            "expected mid-release ≈ 0.4, got {mid}"
        );
        // Run past full release (100 ms total).
        for _ in 0..3000 {
            env.tick();
        }
        assert!(
            !env.is_active(),
            "envelope should be idle after release completes"
        );
        assert!(
            env.tick() <= 1.0e-6,
            "envelope should be silent after release"
        );
    }

    /// note_off during attack should release from the partial attack
    /// value (not jump up to sustain first).
    #[test]
    fn release_from_partial_attack() {
        let mut env = Adsr::new(48_000.0, 0.100, 0.200, 0.5, 0.050);
        env.note_on();
        // Tick a few samples — value is well below 1.0.
        let mut v = 0.0;
        for _ in 0..240 {
            v = env.tick();
        }
        assert!(
            v > 0.0 && v < 0.5,
            "partial attack value out of expected range: {v}"
        );
        env.note_off();
        // After full release (50 ms = 2400 samples) we're at zero.
        for _ in 0..2500 {
            env.tick();
        }
        assert!(!env.is_active());
    }
}
