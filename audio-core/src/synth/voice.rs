//! Single synth voice — three oscillators + noise into a mixer, one
//! filter, one amp envelope, one filter envelope.
//!
//! Stage 3 adds the §1.1 source section: three oscillators (waveform /
//! octave / detune / level each) plus a white-noise source, mixed and
//! renormalized before the ladder filter. Stage 4 adds glide
//! (portamento): a per-voice one-pole slew on the base frequency (see
//! `Voice::tick`). Per-voice LFO routing is a later phase.

use super::envelope::Adsr;
use super::filter::MoogFilter;
use super::oscillator::{OscParams, Oscillator};

/// Convert a MIDI note number to frequency in Hz (A4 = 69 = 440 Hz).
pub fn midi_to_hz(note: u8) -> f32 {
    440.0 * 2.0f32.powf((note as f32 - 69.0) / 12.0)
}

/// Filter-envelope (env 2) parameters, pushed per block from the DSP
/// atomics into each voice.
///
/// ## Cutoff modulation model
///
/// ```text
/// effective_cutoff_hz = base_cutoff * 2^(amount * env_value * 4.0)
/// ```
///
/// i.e. `amount` in 0..=1 sweeps the cutoff up to +4 octaves above the
/// knob cutoff at the envelope peak, clamped to [20, 20000] Hz. With
/// `amount == 0` the exponent is 0 and the effective cutoff equals the
/// knob cutoff exactly — the modulated path is bit-transparent.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct FilterEnvParams {
    pub attack_s: f32,
    pub decay_s: f32,
    pub sustain: f32,
    pub release_s: f32,
    /// Env → cutoff amount in 0..=1 (octaves-of-sweep / 4).
    pub amount: f32,
}

impl FilterEnvParams {
    /// docs/ROADMAP.md §1.4 filter ADSR (5 ms / 600 ms / 0.4 / 600 ms).
    /// `amount` defaults to 0.0 — OFF — so the default render stays
    /// bit-identical to the pre-filter-env engine (regression guarantee).
    /// §1.4's factory "+30%" env amount is a *patch* value; it gets
    /// applied when the patch loader (§3) pushes patch params.
    pub fn default_minimoog() -> Self {
        Self {
            attack_s: 0.005,
            decay_s: 0.600,
            sustain: 0.4,
            release_s: 0.600,
            amount: 0.0,
        }
    }
}

/// Tiny allocation-free xorshift32 white-noise source, one per voice.
/// Deterministically seeded so renders are reproducible in tests. The
/// state advances every active-voice sample regardless of level, so
/// turning the noise knob doesn't change *when* the sequence runs —
/// only whether it is audible.
struct NoiseGen {
    state: u32,
}

impl NoiseGen {
    fn new() -> Self {
        // Any nonzero seed works for xorshift32; this one is arbitrary.
        Self { state: 0x2545_F491 }
    }

    /// One white-noise sample in -1..1.
    #[inline]
    fn tick(&mut self) -> f32 {
        let mut x = self.state;
        x ^= x << 13;
        x ^= x >> 17;
        x ^= x << 5;
        self.state = x;
        (x as f32) * (2.0 / u32::MAX as f32) - 1.0
    }
}

/// One voice. Stage 3 holds three oscillators + a noise source into a
/// renormalized mixer, then a ladder filter, an amp envelope, and a
/// filter envelope. The voice owns its per-instance DSP state and
/// renders one sample at a time.
pub struct Voice {
    /// Currently sounding MIDI note, if any. `None` while idle.
    pub note: Option<u8>,
    /// Velocity-derived linear scale applied to the voice output
    /// (`velocity / 127`). Captured at `note_on` so a velocity-sensitive
    /// patch reacts to the keyboard.
    pub velocity_scale: f32,
    /// Monotonic counter set by the allocator when the voice fires —
    /// later used by the oldest-voice steal policy. Phase 1 doesn't use
    /// this yet, but it costs nothing to track.
    pub fired_at: u64,
    /// The §1.1 source section: three oscillators, each with its own
    /// waveform / octave / detune / mixer level (see `OscParams`).
    oscs: [Oscillator; 3],
    /// White-noise source; mixed in at `noise_level`.
    noise: NoiseGen,
    /// Noise mixer level in 0..=1. 0 (the default) keeps the noise path
    /// silent — regression-safe.
    noise_level: f32,
    filter: MoogFilter,
    amp_env: Adsr,
    /// Filter envelope (env 2) — modulates cutoff per the model on
    /// [`FilterEnvParams`]. Triggered/released alongside `amp_env`.
    filter_env: Adsr,
    /// Env-2 → cutoff amount in 0..=1. 0 disables the per-sample
    /// modulation path entirely.
    filter_env_amount: f32,
    /// Last `FilterEnvParams` pushed by `set_filter_env` — change
    /// detection so the per-block push is free when knobs are idle.
    filter_env_params: FilterEnvParams,
    /// Cached *base* (knob) cutoff so we only call `set_cutoff_q` when it
    /// changes, avoiding redundant work on every block.
    last_cutoff_hz: f32,
    /// Cached resonance for the same reason.
    last_resonance: f32,
    /// Cutoff currently programmed into the ladder — base cutoff when
    /// unmodulated, env-modulated effective cutoff otherwise. The
    /// per-sample modulation path retunes only when the effective value
    /// moves more than ~0.5 Hz from this (cheap hysteresis, avoids
    /// retune spam).
    applied_cutoff_hz: f32,
    /// Sample rate in Hz, kept for the glide-coefficient recompute.
    sample_rate: f32,
    /// Glide (portamento) time constant in seconds, clamped [0, 5].
    /// 0 (the default) disables the slew entirely — the base frequency
    /// jumps straight to the note's pitch, bit-identical to the
    /// pre-glide engine (regression guarantee).
    glide_s: f32,
    /// Cached one-pole slew coefficient `1 - exp(-1/(glide_s * sr))`,
    /// recomputed only when `glide_s` changes. Exactly 1.0 when glide
    /// is off, which `tick` special-cases into a direct jump.
    glide_coeff: f32,
    /// Slewed base frequency state in Hz. Per sample it moves toward
    /// the current note's pitch by `glide_coeff` of the remaining
    /// distance; per-osc octave/detune multipliers apply AFTER the
    /// slew. Snapped to the target in `note_on` when the voice starts
    /// from silence (see there for the rationale).
    current_freq_hz: f32,
}

impl Voice {
    /// Build a voice configured with the Minimoog defaults from doc 14
    /// §4.4 (amp ADSR 5/200/0.7/400, cutoff 2 kHz, resonance 0.3).
    pub fn new(sample_rate: f32) -> Self {
        let cutoff = 2_000.0;
        let resonance = 0.3;
        let fenv = FilterEnvParams::default_minimoog();
        let osc_defaults = OscParams::default_bank();
        Self {
            note: None,
            velocity_scale: 0.0,
            fired_at: 0,
            oscs: osc_defaults.map(|p| Oscillator::new(sample_rate, p)),
            noise: NoiseGen::new(),
            noise_level: 0.0,
            filter: MoogFilter::new(sample_rate, cutoff, resonance),
            amp_env: Adsr::new(sample_rate, 0.005, 0.200, 0.7, 0.400),
            filter_env: Adsr::new(
                sample_rate,
                fenv.attack_s,
                fenv.decay_s,
                fenv.sustain,
                fenv.release_s,
            ),
            filter_env_amount: fenv.amount,
            filter_env_params: fenv,
            last_cutoff_hz: cutoff,
            last_resonance: resonance,
            applied_cutoff_hz: cutoff,
            sample_rate,
            glide_s: 0.0,
            glide_coeff: 1.0,
            // Defined-but-unreachable start value: every path that
            // fires a silent voice goes through `note_on`, which snaps
            // this to the note's pitch before the first `tick`.
            current_freq_hz: 440.0,
        }
    }

    /// Trigger this voice with the given MIDI note + velocity. Both
    /// envelopes (amp + filter) fire together; the mono-legato
    /// suppress-retrigger path in the allocator bypasses this method
    /// entirely, so it suppresses both envelopes as one.
    ///
    /// ## Glide reset policy (stage 4)
    ///
    /// A voice that starts **from silence** begins exactly at its
    /// target pitch — glide only spans intervals within a
    /// continuously-sounding phrase. Deliberate choice: gliding from
    /// the last note of the *previous* phrase (possibly seconds ago)
    /// reads as a surprise swoop after a rest, which no performer
    /// expects. While the voice is still sounding (legato hand-offs,
    /// and retriggers during a release tail), glide always applies —
    /// Minimoog behavior. The idle check reads the amp envelope, not
    /// `self.note`, because the mono allocator assigns `note` directly
    /// before calling this method.
    pub fn note_on(&mut self, note: u8, velocity: u8, fired_at: u64) {
        if !self.amp_env.is_active() {
            self.current_freq_hz = midi_to_hz(note);
        }
        self.note = Some(note);
        self.velocity_scale = (velocity as f32) / 127.0;
        self.fired_at = fired_at;
        self.amp_env.note_on();
        self.filter_env.note_on();
    }

    /// Release this voice — starts both envelopes' release stages.
    pub fn note_off(&mut self) {
        self.amp_env.note_off();
        self.filter_env.note_off();
    }

    /// `true` if the voice is still producing audio. Held for the
    /// Phase 3 voice-stealing allocator; the Phase 1 callers (and the
    /// test below) use it but the release build can't see the test
    /// reference.
    #[allow(dead_code)]
    pub fn is_active(&self) -> bool {
        self.amp_env.is_active()
    }

    /// Per-block setup. Cheap when nothing changed (compares against the
    /// last applied values). This caches the *base* (knob) cutoff; env-2
    /// modulation on top of it happens per-sample inside `tick`, keyed
    /// off `applied_cutoff_hz` so the two paths never fight.
    pub fn set_filter(&mut self, cutoff_hz: f32, resonance: f32) {
        let cutoff_hz = cutoff_hz.clamp(20.0, 20_000.0);
        let resonance = resonance.clamp(0.0, 1.0);
        if (cutoff_hz - self.last_cutoff_hz).abs() > 0.01
            || (resonance - self.last_resonance).abs() > 0.0001
        {
            self.filter.set_cutoff_q(cutoff_hz, resonance);
            self.last_cutoff_hz = cutoff_hz;
            self.last_resonance = resonance;
            self.applied_cutoff_hz = cutoff_hz;
        }
    }

    /// Per-block filter-envelope (env 2) parameter push. Change-detected
    /// so it's free while knobs are idle; updating times/levels does not
    /// disturb a running envelope (see `Adsr::set_params`).
    pub fn set_filter_env(&mut self, params: FilterEnvParams) {
        if params != self.filter_env_params {
            self.filter_env.set_params(
                params.attack_s,
                params.decay_s,
                params.sustain,
                params.release_s,
            );
            self.filter_env_amount = params.amount.clamp(0.0, 1.0);
            self.filter_env_params = params;
        }
    }

    /// Per-block oscillator parameter push, mirroring `set_filter_env`.
    /// Change-detected inside the oscillator so it's free while knobs
    /// are idle. `idx` out of range is ignored (callers validate at the
    /// FFI boundary).
    pub fn set_osc(&mut self, idx: usize, params: OscParams) {
        if let Some(osc) = self.oscs.get_mut(idx) {
            osc.set_params(params);
        }
    }

    /// Per-block noise-level push. Clamped to [0, 1].
    pub fn set_noise_level(&mut self, level: f32) {
        self.noise_level = level.clamp(0.0, 1.0);
    }

    /// Per-block glide-time push, mirroring `set_noise_level`. Clamped
    /// to [0, 5] seconds; change-detected so the transcendental
    /// coefficient recompute only runs when the knob moves. The
    /// coefficient is evaluated in f64 (`exp_m1`) because
    /// `1 - exp(-x)` for the tiny per-sample x of long glides loses
    /// most of its precision in f32.
    pub fn set_glide(&mut self, glide_s: f32) {
        let glide_s = glide_s.clamp(0.0, 5.0);
        if glide_s != self.glide_s {
            self.glide_s = glide_s;
            self.glide_coeff = if glide_s <= 0.0 {
                1.0
            } else {
                (-(-1.0f64 / (glide_s as f64 * self.sample_rate as f64)).exp_m1()) as f32
            };
        }
    }

    /// Render one sample from this voice. Returns `0.0` when the voice
    /// is idle so it can be summed unconditionally with no audible cost.
    pub fn tick(&mut self) -> f32 {
        let Some(note) = self.note else {
            return 0.0;
        };
        // Filter envelope (env 2) → cutoff modulation. See
        // `FilterEnvParams` for the model:
        //   effective = base * 2^(amount * env * 4.0), clamped [20, 20000].
        // The envelope always ticks (so its stage tracks the gate even
        // while amount is 0), but the retune path only runs when the
        // modulation is audible AND the effective cutoff moved > 0.5 Hz
        // since the last retune — cheap hysteresis against retune spam.
        let env2 = self.filter_env.tick();
        let effective_cutoff = if self.filter_env_amount > 0.0 {
            (self.last_cutoff_hz * (self.filter_env_amount * env2 * 4.0).exp2())
                .clamp(20.0, 20_000.0)
        } else {
            self.last_cutoff_hz
        };
        if (effective_cutoff - self.applied_cutoff_hz).abs() > 0.5 {
            self.filter
                .set_cutoff_q(effective_cutoff, self.last_resonance);
            self.applied_cutoff_hz = effective_cutoff;
        }
        // Glide (stage 4): one-pole slew of the base frequency toward
        // the current note's pitch —
        //   current += (target - current) * (1 - exp(-1/(glide_s*sr)))
        // — so `glide_s` is the exponential time constant of the sweep.
        // Per-osc octave/detune multipliers apply AFTER the slew (all
        // three oscillators glide together). With glide off the
        // coefficient is exactly 1.0 and we jump straight to the target
        // pitch, reproducing the pre-glide engine bit for bit
        // (regression guarantee).
        let target_freq = midi_to_hz(note);
        let base_freq = if self.glide_coeff >= 1.0 {
            self.current_freq_hz = target_freq;
            target_freq
        } else {
            self.current_freq_hz += (target_freq - self.current_freq_hz) * self.glide_coeff;
            self.current_freq_hz
        };
        // Source section (§1.1): three oscillators + noise into the
        // mixer. All sources always tick (phase/state continuity), so
        // turning a level knob doesn't jump the others' phases. The mix
        // is renormalized by 1/max(1, Σlevels) so cranking every source
        // to 1 doesn't quadruple the drive into the filter; with the
        // default single-osc levels (Σ = 1) the factor is exactly 1.0
        // and the path is bit-transparent (regression guarantee).
        let mut mix = 0.0f32;
        let mut level_sum = 0.0f32;
        for osc in &mut self.oscs {
            let level = osc.level();
            mix += osc.tick(base_freq) * level;
            level_sum += level;
        }
        mix += self.noise.tick() * self.noise_level;
        level_sum += self.noise_level;
        mix *= 1.0 / level_sum.max(1.0);
        let filtered = self.filter.tick(mix);
        let amp = self.amp_env.tick();
        let out = filtered * amp * self.velocity_scale;
        if !self.amp_env.is_active() {
            // Envelope finished — release the note slot so the allocator
            // can reuse it.
            self.note = None;
            // The filter envelope's release can outlast the amp release
            // (the defaults guarantee it: amp 0.4 s < filter 0.6 s), and
            // once the voice stops ticking env2 would freeze mid-release
            // at a nonzero value. Hard-reset it and retune the ladder to
            // the unmodulated base cutoff so the next note's attack
            // starts from the same contour as a fresh voice instead of
            // being colored by stale envelope state and a stale
            // applied-cutoff cache (the >0.5 Hz retune hysteresis would
            // otherwise keep the stale tuning live into the next note).
            self.filter_env.reset();
            if self.applied_cutoff_hz != self.last_cutoff_hz {
                self.filter
                    .set_cutoff_q(self.last_cutoff_hz, self.last_resonance);
                self.applied_cutoff_hz = self.last_cutoff_hz;
            }
        }
        out
    }

    /// Cutoff currently programmed into the ladder (test probe for the
    /// env-2 modulation contour).
    #[cfg(test)]
    pub(crate) fn applied_cutoff_hz(&self) -> f32 {
        self.applied_cutoff_hz
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn midi_to_hz_a4() {
        let hz = midi_to_hz(69);
        assert!((hz - 440.0).abs() < 0.01, "A4 should be 440 Hz, got {hz}");
    }

    #[test]
    fn midi_to_hz_one_octave_up() {
        let hz = midi_to_hz(81); // A5
        assert!((hz - 880.0).abs() < 0.02, "A5 should be 880 Hz, got {hz}");
    }

    /// Drive a voice through note_on → render → note_off → render and
    /// confirm the envelope contour: rises during attack, plateaus near
    /// the sustain level, decays toward 0 after note_off.
    #[test]
    fn voice_adsr_contour() {
        let sr = 48_000.0_f32;
        let mut v = Voice::new(sr);
        v.note_on(60, 100, 0);

        // Peak amplitude during attack window (5 ms = 240 samples).
        let mut attack_peak: f32 = 0.0;
        for _ in 0..480 {
            attack_peak = attack_peak.max(v.tick().abs());
        }
        assert!(
            attack_peak > 0.05,
            "expected audible attack output, got peak={attack_peak}"
        );

        // Settle into sustain. Amp env target is 0.7 (Minimoog default);
        // velocity scale at vel=100 is 100/127 ≈ 0.787; oscillator output
        // is roughly ±1. Sustain peak therefore lands well under 1.0.
        let mut sustain_peak: f32 = 0.0;
        // Skip the first samples; tick is the filter ringing during decay.
        for _ in 0..24_000 {
            // ~500 ms past decay tail
            sustain_peak = sustain_peak.max(v.tick().abs());
        }
        assert!(
            sustain_peak > 0.05,
            "expected audible sustain output, got peak={sustain_peak}"
        );

        // Release.
        v.note_off();
        // Run past the full release (400 ms = 19_200 samples) plus a tail.
        for _ in 0..24_000 {
            v.tick();
        }
        assert!(
            !v.is_active(),
            "voice should be idle after release completes"
        );
        // Subsequent ticks must be exactly zero (voice slot released).
        for _ in 0..100 {
            let s = v.tick();
            assert!(s == 0.0, "expected silence after release, got {s}");
        }
    }
}
