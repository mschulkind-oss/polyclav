//! Single synth voice — three oscillators + noise into a mixer, one
//! filter, one amp envelope, one filter envelope.
//!
//! Stage 3 adds the §1.1 source section: three oscillators (waveform /
//! octave / detune / level each) plus a white-noise source, mixed and
//! renormalized before the ladder filter. Stage 4 adds glide
//! (portamento): a per-voice one-pole slew on the base frequency (see
//! `Voice::tick`). This stage adds the §1.1 "kbd, vel" mod inputs:
//! velocity → amp / cutoff routing and keyboard tracking of the cutoff
//! (the canonical effective-cutoff formula lives in `Voice::tick`).
//! The GLOBAL block's per-sample modulation (LFO vibrato + pitch bend,
//! LFO → cutoff) arrives as the `pitch_mul` / `cutoff_mul` arguments of
//! `Voice::tick` — the voice itself owns no LFO state (ROADMAP §1.1:
//! the LFO is shared across voices). Stage 5 adds the optional 2×
//! oversampled drive + ladder path (`set_oversample` — see
//! `filter::OversampledDriveLadder`); OFF by default and bit-transparent
//! there.

use super::envelope::Adsr;
use super::filter::{MoogFilter, OversampledDriveLadder};
use super::oscillator::{OscParams, Oscillator};

/// Convert a MIDI note number to frequency in Hz (A4 = 69 = 440 Hz).
pub fn midi_to_hz(note: u8) -> f32 {
    440.0 * 2.0f32.powf((note as f32 - 69.0) / 12.0)
}

/// Amp-envelope (env 1) parameters, pushed per block from the DSP
/// atomics into each voice — same lifecycle as [`FilterEnvParams`].
/// Times in seconds; sustain in 0..=1.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct AmpEnvParams {
    pub attack_s: f32,
    pub decay_s: f32,
    pub sustain: f32,
    pub release_s: f32,
}

impl AmpEnvParams {
    /// docs/ROADMAP.md §1.4 amp ADSR (5 ms / 200 ms / 0.7 / 400 ms) —
    /// exactly the values `Voice::new` hardcoded before the amp env
    /// became a runtime parameter, so the default render stays
    /// bit-identical (regression guarantee).
    pub fn default_minimoog() -> Self {
        Self {
            attack_s: 0.005,
            decay_s: 0.200,
            sustain: 0.7,
            release_s: 0.400,
        }
    }
}

/// Filter-envelope (env 2) parameters, pushed per block from the DSP
/// atomics into each voice.
///
/// Env 2 contributes the `2^(amount * env_value * 4.0)` factor of the
/// canonical effective-cutoff formula (documented in one place — see
/// [`Voice::tick`]): `amount` in 0..=1 sweeps the cutoff up to +4
/// octaves above the knob cutoff at the envelope peak. With
/// `amount == 0` the factor is exactly 1.0 — the modulated path is
/// bit-transparent.
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
    /// Velocity-derived linear scale applied to the voice output:
    /// `lerp(1.0, velocity / 127, vel_to_amp)`. Captured at `note_on`
    /// (via `set_velocity`) so a velocity-sensitive patch reacts to the
    /// keyboard. At the default `vel_to_amp` = 1.0 this is exactly the
    /// classic `velocity / 127` (regression guarantee).
    velocity_scale: f32,
    /// Monotonic counter set by the allocator when the voice fires —
    /// the poly steal policy takes the sounding voice with the lowest
    /// value ("oldest" — see `NativeSynth::poly_voice_index`).
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
    /// Last `AmpEnvParams` pushed by `set_amp_env` — change detection so
    /// the per-block push is free when knobs are idle (mirrors
    /// `filter_env_params`).
    amp_env_params: AmpEnvParams,
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
    /// Pre-filter tanh drive amount in [0, 1] (ROADMAP §1.1
    /// "TANH/SOFTCLIP DRIVE" block, between the mixer and the ladder).
    /// 0 (the default) bypasses the saturator EXACTLY — the mixed
    /// sample is passed to the filter untouched, bit-identical to the
    /// pre-drive engine (regression guarantee). When > 0:
    ///
    /// ```text
    /// g = 1 + drive * 4          (pre-gain, 1..5)
    /// y = tanh(x * g) * (1 / g)  (tanh_norm = g)
    /// ```
    ///
    /// Normalizing by the same `g` used as pre-gain keeps unity gain
    /// for small signals (tanh(x·g) ≈ x·g when |x·g| ≪ 1, so y ≈ x)
    /// while peaks compress toward ±1/g — more drive squashes the
    /// waveform harder without pumping the level into the ladder.
    drive: f32,
    /// Cached pre-gain `1 + drive * 4`, recomputed only when `drive`
    /// changes.
    drive_gain: f32,
    /// Cached `1 / drive_gain` (the tanh_norm reciprocal) so the
    /// per-sample path is a multiply, not a divide.
    drive_norm: f32,
    /// Velocity → amp routing amount in [0, 1] (ROADMAP §1.1 mod input
    /// "vel"). 1.0 (the default) reproduces the classic
    /// `velocity / 127` amp scaling bit for bit (regression guarantee);
    /// 0.0 ignores velocity — every note sounds at full amplitude. Only
    /// read when a note fires (`set_velocity`).
    vel_to_amp: f32,
    /// Velocity → cutoff routing amount in [0, 1] — up to ±1 octave
    /// around the knob cutoff, centered at velocity 64 (see the
    /// canonical formula in `tick`). 0 (the default) keeps the
    /// velocity→cutoff multiplier at exactly 1.0 — bit-transparent.
    /// Only read when a note fires (`set_velocity`).
    vel_to_cutoff: f32,
    /// Velocity → cutoff multiplier
    /// `2^(vel_to_cutoff * (velocity/127 - 0.5) * 2)`, CAPTURED at
    /// `note_on` per voice (like `velocity_scale`) — knob turns
    /// mid-note affect the next note, not the sounding one. Exactly 1.0
    /// while `vel_to_cutoff` is 0.
    vel_cutoff_mul: f32,
    /// Keyboard-tracking amount in [0, 1] (ROADMAP §1.1 mod input
    /// "kbd"): the cutoff tracks the keyboard by
    /// `2^(kbd_track * (note - 60) / 12)` (see the canonical formula in
    /// `tick`). 0 (the default) keeps the tracking multiplier at
    /// exactly 1.0 — bit-transparent.
    kbd_track: f32,
    /// Cached keyboard-tracking multiplier for (`kbd_track`,
    /// `kbd_mul_note`). Recomputed in `tick` when the sounding note
    /// changes (mono legato hand-offs reassign `note` without a
    /// `note_on`, and tracking follows the *sounding* note) and in
    /// `set_kbd_track` when the knob moves.
    kbd_mul: f32,
    /// The note `kbd_mul` was computed for. Initialized to 60 — the
    /// tracking pivot, where the multiplier is exactly 1.0 for any
    /// amount, matching the initial `kbd_mul`.
    kbd_mul_note: u8,
    /// 2× oversampling of the nonlinear section (drive + ladder),
    /// ROADMAP §0.1 / §1.6 / Appendix A pivot item (a). `false` (the
    /// default) keeps the historic base-rate path bit for bit — the
    /// oversampled objects below are never ticked (regression
    /// guarantee). When `true`, `tick` routes the mixer output through
    /// `over_ladder` instead of the inline drive + `filter`.
    oversample: bool,
    /// The oversampled drive + ladder (its inner node runs at
    /// `sample_rate * 2`, coefficients retuned for that rate — see
    /// `filter::OversampledDriveLadder`). A SECOND filter instance next
    /// to `filter`, swapped in/out on toggle: the newly-active path has
    /// its state reset and its tuning synced at the swap, which can
    /// step the output for one sample — a brief, documented click risk
    /// (see `set_oversample`).
    over_ladder: OversampledDriveLadder,
}

impl Voice {
    /// Build a voice configured with the Minimoog defaults from doc 14
    /// §4.4 (amp ADSR 5/200/0.7/400, cutoff 2 kHz, resonance 0.3).
    pub fn new(sample_rate: f32) -> Self {
        let cutoff = 2_000.0;
        let resonance = 0.3;
        let aenv = AmpEnvParams::default_minimoog();
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
            amp_env: Adsr::new(
                sample_rate,
                aenv.attack_s,
                aenv.decay_s,
                aenv.sustain,
                aenv.release_s,
            ),
            amp_env_params: aenv,
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
            drive: 0.0,
            drive_gain: 1.0,
            drive_norm: 1.0,
            vel_to_amp: 1.0,
            vel_to_cutoff: 0.0,
            vel_cutoff_mul: 1.0,
            kbd_track: 0.0,
            kbd_mul: 1.0,
            kbd_mul_note: 60,
            oversample: false,
            over_ladder: OversampledDriveLadder::new(sample_rate, cutoff, resonance),
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
        self.set_velocity(velocity);
        self.fired_at = fired_at;
        self.amp_env.note_on();
        self.filter_env.note_on();
    }

    /// Capture the per-note velocity-derived state: the amp scale
    /// `lerp(1.0, velocity/127, vel_to_amp)` and the velocity→cutoff
    /// multiplier `2^(vel_to_cutoff * (velocity/127 - 0.5) * 2)`.
    /// Called by `note_on` AND by the mono allocator's legato hand-off
    /// path (which updates the velocity without retriggering the
    /// envelopes). At `vel_to_amp` == 1.0 (the default) the amp scale
    /// is exactly `velocity / 127` — bit-identical to the pre-routing
    /// engine (regression guarantee); at `vel_to_cutoff` == 0 (the
    /// default) the cutoff multiplier is exactly 1.0.
    pub fn set_velocity(&mut self, velocity: u8) {
        let vel_norm = (velocity as f32) / 127.0;
        self.velocity_scale = if self.vel_to_amp >= 1.0 {
            // Hard branch instead of the lerp: `1 + (v - 1) * 1` is not
            // bit-exactly `v` for v < 0.5 in f32.
            vel_norm
        } else {
            1.0 + (vel_norm - 1.0) * self.vel_to_amp
        };
        self.vel_cutoff_mul = if self.vel_to_cutoff <= 0.0 {
            1.0
        } else {
            (self.vel_to_cutoff * (vel_norm - 0.5) * 2.0).exp2()
        };
    }

    /// Release this voice — starts both envelopes' release stages.
    pub fn note_off(&mut self) {
        self.amp_env.note_off();
        self.filter_env.note_off();
    }

    /// `true` if the voice is still producing audio. The poly
    /// allocator's free-voice search keys off this ("free" = amp env
    /// idle — see `NativeSynth::poly_voice_index`).
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
            self.retune_active_ladder(cutoff_hz, resonance);
            self.last_cutoff_hz = cutoff_hz;
            self.last_resonance = resonance;
            self.applied_cutoff_hz = cutoff_hz;
        }
    }

    /// Retune whichever ladder is currently in the signal path (the
    /// base-rate `filter`, or `over_ladder` when the 2× oversampled
    /// path is engaged — its coefficients are computed for the doubled
    /// rate). The idle path's tuning is synced once at the next toggle
    /// (`set_oversample`), so each cutoff move costs exactly one
    /// retune, never two.
    fn retune_active_ladder(&mut self, cutoff_hz: f32, q: f32) {
        if self.oversample {
            self.over_ladder.set_cutoff_q(cutoff_hz, q);
        } else {
            self.filter.set_cutoff_q(cutoff_hz, q);
        }
    }

    /// Per-block 2× oversampling toggle (ROADMAP §0.1 / §1.6 —
    /// oversample the nonlinear drive + ladder section). Change-detected
    /// so the per-block push is free; `false` (the default) never
    /// touches the oversampled objects, keeping the base-rate render
    /// bit-identical (regression guarantee).
    ///
    /// ## Toggle mid-note: the documented click risk
    ///
    /// The two paths are separate filter instances with independent
    /// state, so a swap while a voice sounds cannot hand the ladder
    /// state over (the state variables live at different sample rates
    /// and aren't interchangeable). Instead the newly-active path is
    /// RESET (zeroed FIR history / ladder state) and retuned to the
    /// current applied cutoff. The output therefore steps at the swap
    /// boundary — a brief click may be audible on a sounding voice.
    /// Accepted trade-off: the toggle is a sound-design/setup switch,
    /// not a performance control.
    pub fn set_oversample(&mut self, on: bool) {
        if on == self.oversample {
            return;
        }
        self.oversample = on;
        if on {
            self.over_ladder.reset();
            // Drive gain is mirrored into the wrapper on every
            // `set_drive` change, so only the tuning needs syncing
            // here (the inactive path skips retunes — see
            // `retune_active_ladder`).
            self.over_ladder
                .set_cutoff_q(self.applied_cutoff_hz, self.last_resonance);
        } else {
            self.filter.reset();
            self.filter
                .set_cutoff_q(self.applied_cutoff_hz, self.last_resonance);
        }
    }

    /// Per-block amp-envelope (env 1) parameter push. Change-detected
    /// so it's free while knobs are idle; updating times/levels does not
    /// disturb a running envelope (see `Adsr::set_params`). At the
    /// §1.4 defaults (5 ms / 200 ms / 0.7 / 400 ms — the values
    /// `Voice::new` bakes in) the push is a no-op, keeping the default
    /// render bit-identical (regression guarantee).
    pub fn set_amp_env(&mut self, params: AmpEnvParams) {
        if params != self.amp_env_params {
            self.amp_env.set_params(
                params.attack_s,
                params.decay_s,
                params.sustain,
                params.release_s,
            );
            self.amp_env_params = params;
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

    /// Per-block pulse-width push, fanned out to all three oscillators
    /// (the width is a single global knob, Minimoog-style). Clamped to
    /// [0.05, 0.95] inside each oscillator; only audible while a pulse
    /// waveform is selected.
    pub fn set_pulse_width(&mut self, width: f32) {
        for osc in &mut self.oscs {
            osc.set_pulse_width(width);
        }
    }

    /// Per-block pre-filter drive push. Clamped to [0, 1];
    /// change-detected so the gain/norm recompute (one divide) only
    /// runs when the knob moves. 0 (the default) bypasses the tanh
    /// stage exactly — see the `drive` field docs for the formula.
    pub fn set_drive(&mut self, drive: f32) {
        let drive = drive.clamp(0.0, 1.0);
        if drive != self.drive {
            self.drive = drive;
            self.drive_gain = 1.0 + drive * 4.0;
            self.drive_norm = 1.0 / self.drive_gain;
            // Mirror into the oversampled wrapper (its tanh runs at 2×
            // when that path is engaged) so the two paths always agree
            // on the drive model — cheap, only on knob movement.
            self.over_ladder
                .set_drive_gain(self.drive_gain, self.drive_norm);
        }
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

    /// Per-block velocity-routing push (ROADMAP §1.1 mod input "vel"),
    /// mirroring `set_noise_level`. Both amounts clamp to [0, 1]. The
    /// amounts only feed state CAPTURED at note fire time
    /// (`set_velocity`) — turning these knobs mid-note affects the next
    /// note, not the sounding one.
    pub fn set_vel_routing(&mut self, to_cutoff: f32, to_amp: f32) {
        self.vel_to_cutoff = to_cutoff.clamp(0.0, 1.0);
        self.vel_to_amp = to_amp.clamp(0.0, 1.0);
    }

    /// Per-block keyboard-tracking push (ROADMAP §1.1 mod input "kbd"),
    /// mirroring `set_noise_level`. Clamped to [0, 1]; change-detected
    /// so the exp2 recompute only runs when the knob moves. Unlike the
    /// velocity routing this is NOT captured per note — the tracking
    /// multiplier follows the sounding note and the live knob.
    pub fn set_kbd_track(&mut self, amt: f32) {
        let amt = amt.clamp(0.0, 1.0);
        if amt != self.kbd_track {
            self.kbd_track = amt;
            self.kbd_mul = Self::kbd_mul_for(amt, self.kbd_mul_note);
        }
    }

    /// Keyboard-tracking cutoff multiplier
    /// `2^(kbd_track * (note - 60) / 12)` — exactly 1.0 when tracking
    /// is off (bit-transparent bypass; see the canonical formula in
    /// `tick`).
    fn kbd_mul_for(kbd_track: f32, note: u8) -> f32 {
        if kbd_track <= 0.0 {
            1.0
        } else {
            (kbd_track * ((note as f32) - 60.0) / 12.0).exp2()
        }
    }

    /// Render one sample from this voice. Returns `0.0` when the voice
    /// is idle so it can be summed unconditionally with no audible cost.
    ///
    /// `pitch_mul` and `cutoff_mul` carry the GLOBAL block's per-sample
    /// modulation (computed once per sample by `NativeSynth::render`
    /// and fanned out to every voice):
    ///
    /// - `pitch_mul` — pitch-bend factor × LFO vibrato factor,
    ///   multiplied onto the post-glide base frequency (after the slew,
    ///   so vibrato isn't smeared by the glide one-pole and bend acts
    ///   instantly mid-glide; before the per-osc octave/detune factors,
    ///   so all three oscillators wobble together).
    /// - `cutoff_mul` — LFO → cutoff factor, composed into the
    ///   canonical effective-cutoff product below.
    ///
    /// Both are exactly 1.0 while their sources are inert, and ×1.0 is
    /// bit-exact — the default render is unchanged (regression
    /// guarantee).
    pub fn tick(&mut self, pitch_mul: f32, cutoff_mul: f32) -> f32 {
        let Some(note) = self.note else {
            return 0.0;
        };
        // ── Effective cutoff: the canonical formula ──────────────────
        // Four multiplicative modulations compose on top of the knob
        // (base) cutoff (ROADMAP §1.1: "cutoff(env, kbd, LFO, vel,
        // knob)"):
        //
        //   effective_cutoff_hz = base_cutoff_hz
        //     * 2^(filter_env_amount * env2 * 4.0)          [env 2: up to +4 oct]
        //     * 2^(vel_to_cutoff * (vel/127 - 0.5) * 2.0)   [velocity: ±1 oct around
        //                                                    vel 64, captured at note_on]
        //     * 2^(kbd_track * (note - 60) / 12.0)          [keyboard tracking]
        //     * cutoff_mul                                  [global LFO → cutoff,
        //                                                    2^(lfo * oct) upstream]
        //   clamped to [20, 20000] Hz.
        //
        // Every factor is exactly 1.0 while its knob is 0 (and ×1.0 is
        // bit-exact), and the whole modulated branch is skipped when all
        // of them are inert, so the default render stays bit-identical
        // (regression guarantee). The envelope always ticks (its stage
        // must track the gate even while inert), and the retune only
        // runs when the effective cutoff moved > 0.5 Hz since the last
        // retune — cheap hysteresis against retune spam.
        if note != self.kbd_mul_note {
            // Mono legato hand-offs reassign `note` without a note_on;
            // the keyboard-tracking multiplier follows the sounding note.
            self.kbd_mul_note = note;
            self.kbd_mul = Self::kbd_mul_for(self.kbd_track, note);
        }
        let env2 = self.filter_env.tick();
        let note_mul = self.vel_cutoff_mul * self.kbd_mul * cutoff_mul;
        let effective_cutoff = if self.filter_env_amount > 0.0 || note_mul != 1.0 {
            (self.last_cutoff_hz * (self.filter_env_amount * env2 * 4.0).exp2() * note_mul)
                .clamp(20.0, 20_000.0)
        } else {
            self.last_cutoff_hz
        };
        if (effective_cutoff - self.applied_cutoff_hz).abs() > 0.5 {
            self.retune_active_ladder(effective_cutoff, self.last_resonance);
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
        // Global pitch modulation (bend × vibrato) applies AFTER the
        // glide slew — the slew state tracks the note's own pitch, so
        // vibrato isn't low-passed by the glide coefficient and a bend
        // mid-glide still acts instantly. Exactly 1.0 (bit-exact) while
        // no bend/vibrato is active.
        let base_freq = base_freq * pitch_mul;
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
        // Pre-filter tanh drive (§1.1 "TANH/SOFTCLIP DRIVE" block):
        //   g = 1 + drive*4;  y = tanh(x*g) * (1/g)
        // — unity gain for small signals (tanh_norm = g), peaks
        // compressed toward ±1/g. drive == 0 skips the stage entirely
        // so the bypass is bit-exact (regression guarantee); note the
        // deliberate hard boundary at 0: drive = ε engages tanh at
        // pre-gain ≈ 1, a barely-audible softening.
        //
        // With the 2× oversampled path engaged, the whole nonlinear
        // section (that same tanh + the ladder) runs inside the
        // halfband up/down wrapper at sample_rate × 2 instead — the
        // tanh's fold-back aliases land above the decimator's stopband
        // and are removed (ROADMAP §0.1 / §1.6). OFF (the default)
        // takes the historic branch below, bit for bit.
        let filtered = if self.oversample {
            self.over_ladder.tick(mix)
        } else {
            if self.drive > 0.0 {
                mix = (mix * self.drive_gain).tanh() * self.drive_norm;
            }
            self.filter.tick(mix)
        };
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
                self.retune_active_ladder(self.last_cutoff_hz, self.last_resonance);
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
            attack_peak = attack_peak.max(v.tick(1.0, 1.0).abs());
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
            sustain_peak = sustain_peak.max(v.tick(1.0, 1.0).abs());
        }
        assert!(
            sustain_peak > 0.05,
            "expected audible sustain output, got peak={sustain_peak}"
        );

        // Release.
        v.note_off();
        // Run past the full release (400 ms = 19_200 samples) plus a tail.
        for _ in 0..24_000 {
            v.tick(1.0, 1.0);
        }
        assert!(
            !v.is_active(),
            "voice should be idle after release completes"
        );
        // Subsequent ticks must be exactly zero (voice slot released).
        for _ in 0..100 {
            let s = v.tick(1.0, 1.0);
            assert!(s == 0.0, "expected silence after release, got {s}");
        }
    }
}
