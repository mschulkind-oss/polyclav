//! Native pure-Rust analog-style synth backend.
//!
//! Phase 1+ of `docs/ROADMAP.md`: a single hardcoded "minimoog" engine —
//! three PolyBLEP oscillators (saw/square/pulse, per-osc octave/detune/
//! level) plus a white-noise source into a renormalized mixer, into a
//! Moog ladder filter (with resonance, filter envelope, velocity →
//! cutoff routing, and keyboard tracking) into an ADSR amp env (with
//! velocity → amp routing) — rendered into the same interleaved stereo
//! buffer the rest of `audio-core` uses. Glide (portamento) slews the
//! pitch. The §1.1 GLOBAL block is live: one LFO shared across voices
//! (triangle / saw / square / S&H — see `lfo.rs`) with three depth
//! knobs (→ pitch vibrato, scaled live by the mod wheel; → cutoff;
//! → amp tremolo), plus pitch bend with a configurable semitone range.
//! At the defaults (osc 2/3 + noise levels 0, all LFO depths 0, no
//! CC/bend events) the render is bit-identical to the Phase 1
//! single-saw engine.
//!
//! ## Mod wheel × vibrato depth (documented decision)
//!
//! The pitch-vibrato depth heard is `mod_wheel × lfo_to_pitch_cents` —
//! the wheel and the configured depth MULTIPLY. The synth **boots with
//! `mod_wheel = 1.0`**, so a configured vibrato depth is audible even
//! on setups with no physical mod wheel; the first incoming CC 1 event
//! then takes over (wheel back to 0 silences vibrato, classic
//! vibrato-on-wheel performance behavior). Both factors at their
//! defaults (wheel 1.0 × depth 0.0) give zero cents — bit-transparent.
//!
//! ## Voice allocator (ROADMAP §1.2 / §1.5)
//!
//! The pool is fixed at 8 voices and the mode is runtime-switchable
//! (per-block atomic push, wire encoding 0/1/2 — see
//! [`NativeSynth::set_voice_mode`]):
//!
//! - **mono_legato** (0, the DEFAULT — bit-identical to the historic
//!   engine): 1 voice, last-note priority, envelopes only retrigger
//!   when no other key is held.
//! - **mono_retrig** (1): 1 voice, last-note priority, envelopes ALWAYS
//!   retrigger on note-on.
//! - **poly** (2): a note-on takes a free voice (amp env idle) or, when
//!   all 8 sound, steals the voice with the LOWEST `fired_at` (the
//!   "oldest" v1 policy of §1.2); a note-off releases exactly the
//!   voice(s) sounding that note.
//!
//! Switching modes while notes sound releases every voice and clears
//! the held-notes stack (documented on `set_voice_mode` — no stuck
//! notes). The GLOBAL block (LFO / mod wheel / pitch bend) is computed
//! once per sample in `render` and fanned out to every sounding voice.

mod envelope;
mod filter;
mod lfo;
mod oscillator;
mod voice;

use crate::MidiEvent;
use lfo::{Lfo, LfoWave};
use oscillator::{OscParams, Waveform, DEFAULT_PULSE_WIDTH};
use voice::{AmpEnvParams, FilterEnvParams, Voice};

/// Maximum voices held in the allocator's pool — the §1.5 poly cap.
/// Mono modes only ever fire voice 0; idle voices tick to exactly 0.0,
/// so the unused pool is free (and bit-transparent: summing +0.0 never
/// changes a sample's bits).
const MAX_VOICES: usize = 8;

/// Voice allocation strategy (ROADMAP §1.2 / §1.5), runtime-switchable
/// via the `native_voice_mode` DSP atomic (see
/// [`NativeSynth::set_voice_mode`]).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum VoiceMode {
    /// Single voice, last-note priority, envelopes only retrigger when
    /// no note is currently held. The default — and the historic
    /// Phase 1 behavior, bit for bit.
    MonoLegato,
    /// Single voice, last-note priority, envelopes ALWAYS retrigger on
    /// note-on (the `Adsr` re-enters its attack stage from the current
    /// value — an audible swell toward peak, no click).
    MonoRetrig,
    /// Up to [`MAX_VOICES`] voices. Note-on takes a free voice (amp env
    /// idle) or steals the oldest-fired sounding voice (lowest
    /// `fired_at`); note-off releases exactly the voice(s) sounding
    /// that note.
    Poly,
}

impl VoiceMode {
    /// Wire encoding shared by the DSP atomic, the FFI setter and the
    /// Go wrapper: 0 = mono_legato, 1 = mono_retrig, 2 = poly. Invalid
    /// codes return `None` (the FFI setter rejects them upstream,
    /// mirroring the osc / LFO wave codes).
    pub fn from_u32(v: u32) -> Option<Self> {
        match v {
            0 => Some(VoiceMode::MonoLegato),
            1 => Some(VoiceMode::MonoRetrig),
            2 => Some(VoiceMode::Poly),
            _ => None,
        }
    }
}

/// One factory preset. Phase 1 ships only `"minimoog"`; future engines
/// (`"fm"`, `"plaits"`, ...) extend this enum.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Engine {
    Minimoog,
}

impl Engine {
    fn parse(name: &str) -> Result<Self, String> {
        match name {
            "minimoog" => Ok(Engine::Minimoog),
            other => Err(format!(
                "unknown native synth engine {other:?} (valid: minimoog)"
            )),
        }
    }
}

/// The native synth backend.
pub struct NativeSynth {
    engine: Engine,
    sample_rate: f32,
    voice_mode: VoiceMode,
    voices: Vec<Voice>,
    /// Stack of currently-held MIDI notes (for mono-legato last-note
    /// priority). Most recently pressed is at the back.
    held_notes: Vec<u8>,
    /// Monotonic counter bumped per voice fire — used by the (future)
    /// oldest-voice steal policy.
    next_fire_id: u64,
    /// Cutoff in Hz, last value applied to voices. Updated per-block
    /// from the audio thread's atomic; the voice's own change detection
    /// avoids redundant Moog retunes.
    cutoff_hz: f32,
    resonance: f32,
    /// Amp-envelope (env 1) parameters, last values applied to voices.
    /// Same per-block push lifecycle as `cutoff_hz`; the voice's own
    /// change detection makes the push free when idle. Defaults to the
    /// §1.4 amp ADSR (5 ms / 200 ms / 0.7 / 400 ms) — the values the
    /// voice previously hardcoded, so the default render is unchanged.
    amp_env: AmpEnvParams,
    /// Filter-envelope (env 2) parameters, last values applied to
    /// voices. Same per-block push lifecycle as `cutoff_hz`; the
    /// voice's own change detection makes the push free when idle.
    filter_env: FilterEnvParams,
    /// Per-oscillator (wave / octave / detune / level) parameters, last
    /// values applied to voices. Same per-block push lifecycle as
    /// `cutoff_hz`.
    osc_params: [OscParams; 3],
    /// Noise mixer level, same lifecycle. Default 0.0 (regression-safe).
    noise_level: f32,
    /// Glide (portamento) time constant in seconds, same lifecycle.
    /// Default 0.0 — no slew, pitch jumps instantly (regression-safe).
    glide_s: f32,
    /// Pulse-wave duty cycle in [0.05, 0.95], same lifecycle. Default
    /// 0.25 — exactly the old fixed stage-3 constant (regression-safe).
    pulse_width: f32,
    /// Pre-filter tanh drive amount in [0, 1], same lifecycle. Default
    /// 0.0 — the drive stage is bypassed bit-exactly (regression-safe).
    /// See `Voice::set_drive` / the voice `drive` field for the model.
    drive: f32,
    /// Velocity → cutoff routing amount in [0, 1], same lifecycle.
    /// Default 0.0 — bypass (bit-transparent). Captured per voice at
    /// note_on; see the canonical effective-cutoff formula in
    /// `Voice::tick`.
    vel_to_cutoff: f32,
    /// Velocity → amp routing amount in [0, 1], same lifecycle. Default
    /// 1.0 — exactly the classic `velocity / 127` amp scaling
    /// (regression-safe); 0 ignores velocity.
    vel_to_amp: f32,
    /// Keyboard-tracking amount in [0, 1], same lifecycle. Default 0.0
    /// — bypass (bit-transparent). See the canonical effective-cutoff
    /// formula in `Voice::tick`.
    kbd_track: f32,
    /// 2× oversampling of the per-voice nonlinear section (drive +
    /// ladder), same lifecycle. Default `false` — the base-rate path,
    /// bit-identical to the historic engine (regression guarantee).
    /// See `Voice::set_oversample` for the swap semantics (and its
    /// documented mid-note click risk).
    oversample: bool,
    /// The GLOBAL LFO (ROADMAP §1.1), shared across voices and advanced
    /// exactly once per output sample in `render`. Free-runs even while
    /// every depth is 0 so engaging a depth doesn't jump the phase.
    lfo: Lfo,
    /// LFO → pitch depth in cents, [0, 100], same lifecycle as
    /// `cutoff_hz`. Default 0.0 — bit-transparent. The audible depth is
    /// scaled live by `mod_wheel` (see the module docs): the voice
    /// frequency is multiplied by
    /// `2^(lfo * mod_wheel * lfo_to_pitch_cents / 1200)`.
    lfo_to_pitch_cents: f32,
    /// LFO → cutoff depth in octaves, [0, 2], same lifecycle. Default
    /// 0.0 — bit-transparent. The effective cutoff is multiplied by
    /// `2^(lfo * lfo_to_cutoff_oct)` (composed into the canonical
    /// formula in `Voice::tick`).
    lfo_to_cutoff_oct: f32,
    /// LFO → amp (tremolo) depth in [0, 1], same lifecycle. Default
    /// 0.0 — bit-transparent. The summed voice output is multiplied by
    /// `1 - depth * (lfo * 0.5 + 0.5)` — a unipolar dip from unity, so
    /// depth 1 swings between full level (LFO trough) and silence (LFO
    /// peak).
    lfo_to_amp: f32,
    /// Mod wheel position in [0, 1], updated live from MIDI CC 1 by
    /// `handle_event`. **Boots at 1.0** so a configured vibrato depth
    /// works without a wheel; the first CC 1 event takes over (see the
    /// module docs for the wheel × depth decision).
    mod_wheel: f32,
    /// Normalized pitch-bend position in [-1, 1] from the 14-bit wire
    /// value: `(bend - 8192) / 8192`. 0 (center, the boot value) is no
    /// bend.
    bend_norm: f32,
    /// Pitch-bend range in semitones at full deflection, [0, 12], same
    /// per-block push lifecycle as `cutoff_hz`. Default 2.0 — the MIDI
    /// convention.
    bend_range_semitones: f32,
    /// Cached bend factor `2^(bend_range_semitones * bend_norm / 12)`,
    /// recomputed only when a bend event arrives or the range knob
    /// moves. Exactly 1.0 at rest (bend_norm 0 ⇒ exp2(0) == 1.0) —
    /// bit-transparent.
    bend_factor: f32,
}

impl NativeSynth {
    /// Build a synth for the given engine name. `sample_rate` is in Hz.
    pub fn new(engine_name: &str, sample_rate: f32) -> Result<Self, String> {
        let engine = Engine::parse(engine_name)?;
        let voices = (0..MAX_VOICES).map(|_| Voice::new(sample_rate)).collect();
        let mut synth = Self {
            engine,
            sample_rate,
            // Minimoog boots in mono-legato (honest to the source —
            // ROADMAP §1.4). Runtime-switchable per block from the
            // `native_voice_mode` DSP atomic via `set_voice_mode`.
            voice_mode: VoiceMode::MonoLegato,
            voices,
            held_notes: Vec::with_capacity(16),
            next_fire_id: 0,
            cutoff_hz: 2_000.0,
            resonance: 0.3,
            amp_env: AmpEnvParams::default_minimoog(),
            filter_env: FilterEnvParams::default_minimoog(),
            osc_params: OscParams::default_bank(),
            noise_level: 0.0,
            glide_s: 0.0,
            pulse_width: DEFAULT_PULSE_WIDTH,
            drive: 0.0,
            vel_to_cutoff: 0.0,
            vel_to_amp: 1.0,
            kbd_track: 0.0,
            oversample: false,
            lfo: Lfo::new(sample_rate),
            lfo_to_pitch_cents: 0.0,
            lfo_to_cutoff_oct: 0.0,
            lfo_to_amp: 0.0,
            // Boot value 1.0 — configured vibrato depth is audible
            // without a mod wheel; the first CC 1 takes over. See the
            // module docs.
            mod_wheel: 1.0,
            bend_norm: 0.0,
            bend_range_semitones: 2.0,
            bend_factor: 1.0,
        };
        // Apply default per-engine voice config. Phase 1 has only one
        // engine; this match exists to make adding more obvious.
        match synth.engine {
            Engine::Minimoog => {
                synth.cutoff_hz = 2_000.0;
                synth.resonance = 0.3;
            }
        }
        Ok(synth)
    }

    /// Set the filter cutoff in Hz. Called from the audio thread once
    /// per block (the atomic-driven Phase 1 knob-4 override).
    pub fn set_cutoff_hz(&mut self, cutoff_hz: f32) {
        self.cutoff_hz = cutoff_hz.clamp(20.0, 20_000.0);
    }

    /// Set the filter resonance (Q), mirroring `set_cutoff_hz`: called
    /// from the audio thread once per block from the DSP atomic.
    /// Clamped to [0.0, 0.95] — headroom below the self-oscillation
    /// instability of the Stilson/Smith ladder.
    pub fn set_resonance(&mut self, resonance: f32) {
        self.resonance = resonance.clamp(0.0, 0.95);
    }

    /// Set the amp-envelope (env 1) parameters, mirroring
    /// `set_cutoff_hz`: called from the audio thread once per block from
    /// the DSP atomics. Times are clamped to [0.0001, 10] s, sustain to
    /// [0, 1] — the same clamps as the filter env. Updating params does
    /// not disturb a running envelope (see `Adsr::set_params`); at the
    /// defaults (5 ms / 200 ms / 0.7 / 400 ms) the push is
    /// bit-transparent.
    pub fn set_amp_env(&mut self, attack_s: f32, decay_s: f32, sustain: f32, release_s: f32) {
        self.amp_env = AmpEnvParams {
            attack_s: attack_s.clamp(1.0e-4, 10.0),
            decay_s: decay_s.clamp(1.0e-4, 10.0),
            sustain: sustain.clamp(0.0, 1.0),
            release_s: release_s.clamp(1.0e-4, 10.0),
        };
    }

    /// Set the filter-envelope (env 2) parameters, mirroring
    /// `set_cutoff_hz`: called from the audio thread once per block from
    /// the DSP atomics. Times are clamped to [0.0001, 10] s, sustain and
    /// amount to [0, 1]. `amount` scales the env-2 → cutoff modulation
    /// (see `voice::FilterEnvParams` for the exponential model); 0 keeps
    /// the modulated path bit-transparent.
    pub fn set_filter_env(
        &mut self,
        attack_s: f32,
        decay_s: f32,
        sustain: f32,
        release_s: f32,
        amount: f32,
    ) {
        self.filter_env = FilterEnvParams {
            attack_s: attack_s.clamp(1.0e-4, 10.0),
            decay_s: decay_s.clamp(1.0e-4, 10.0),
            sustain: sustain.clamp(0.0, 1.0),
            release_s: release_s.clamp(1.0e-4, 10.0),
            amount: amount.clamp(0.0, 1.0),
        };
    }

    /// Set one oscillator's parameters, mirroring `set_cutoff_hz`:
    /// called from the audio thread once per block from the DSP atomics.
    /// `wave` is the 0/1/2 wire encoding (saw/square/pulse — invalid
    /// values fall back to saw; the FFI setter rejects them upstream),
    /// `octave` is clamped to [-2, 2], `detune_cents` to [-100, 100],
    /// `level` to [0, 1]. `idx` out of range is ignored (validated with
    /// an eprintln at the FFI boundary).
    pub fn set_osc(&mut self, idx: usize, wave: u32, octave: i32, detune_cents: f32, level: f32) {
        let Some(slot) = self.osc_params.get_mut(idx) else {
            return;
        };
        *slot = OscParams {
            wave: Waveform::from_u32(wave).unwrap_or(Waveform::Saw),
            octave: octave.clamp(-2, 2),
            detune_cents: detune_cents.clamp(-100.0, 100.0),
            level: level.clamp(0.0, 1.0),
        };
    }

    /// Set the noise mixer level, same lifecycle as `set_cutoff_hz`.
    /// Clamped to [0, 1]; 0 (the default) keeps the noise path silent.
    pub fn set_noise_level(&mut self, level: f32) {
        self.noise_level = level.clamp(0.0, 1.0);
    }

    /// Set the pulse-wave duty cycle, same lifecycle as `set_cutoff_hz`.
    /// One global knob shared by all three oscillators (Minimoog-style);
    /// clamped to [0.05, 0.95]. Only audible while a pulse waveform is
    /// selected — 0.25 (the default) reproduces the old fixed duty
    /// bit for bit.
    pub fn set_pulse_width(&mut self, width: f32) {
        self.pulse_width = width.clamp(0.05, 0.95);
    }

    /// Set the pre-filter tanh drive amount, same lifecycle as
    /// `set_cutoff_hz`. Clamped to [0, 1]; 0 (the default) bypasses the
    /// saturator bit-exactly. When > 0 the mixed signal is shaped by
    /// `tanh(x * (1 + drive*4)) / (1 + drive*4)` before the ladder —
    /// unity gain at small signals, peaks compressed toward
    /// ±1/(1 + drive*4) (ROADMAP §1.1 "TANH/SOFTCLIP DRIVE").
    pub fn set_drive(&mut self, drive: f32) {
        self.drive = drive.clamp(0.0, 1.0);
    }

    /// Set the glide (portamento) time constant in seconds, same
    /// lifecycle as `set_cutoff_hz`. Clamped to [0, 5]. 0 (the default)
    /// disables the frequency slew — pitch jumps instantly, matching
    /// the pre-glide engine bit for bit. When enabled, glide applies to
    /// every pitch transition of a continuously-sounding voice: legato
    /// hand-offs AND retriggered notes (Minimoog behavior). A voice
    /// that starts from silence begins at its target pitch (see
    /// `Voice::note_on` for the rationale).
    pub fn set_glide(&mut self, glide_s: f32) {
        self.glide_s = glide_s.clamp(0.0, 5.0);
    }

    /// Set the velocity-routing amounts (ROADMAP §1.1 mod input "vel"),
    /// same lifecycle as `set_cutoff_hz`. Both clamp to [0, 1].
    /// `to_amp` = 1 (the default) keeps the classic `velocity / 127`
    /// amp scaling bit for bit (regression-safe); 0 ignores velocity.
    /// `to_cutoff` = 0 (the default) is bit-transparent; 1 swings the
    /// effective cutoff up to ±1 octave around the knob cutoff,
    /// centered at velocity 64 — captured per voice at note_on (see
    /// `Voice::tick` for the canonical formula).
    pub fn set_vel_routing(&mut self, to_cutoff: f32, to_amp: f32) {
        self.vel_to_cutoff = to_cutoff.clamp(0.0, 1.0);
        self.vel_to_amp = to_amp.clamp(0.0, 1.0);
    }

    /// Set the keyboard-tracking amount (ROADMAP §1.1 mod input "kbd"),
    /// same lifecycle as `set_cutoff_hz`. Clamped to [0, 1]. 0 (the
    /// default) is bit-transparent; 1 makes the effective cutoff track
    /// the keyboard at 100% (2× per octave above note 60, ÷2 per octave
    /// below — see `Voice::tick` for the canonical formula).
    pub fn set_kbd_track(&mut self, amt: f32) {
        self.kbd_track = amt.clamp(0.0, 1.0);
    }

    /// Enable/disable 2× oversampling of the nonlinear section (drive +
    /// Moog ladder) in every voice (ROADMAP §0.1 / §1.6 / Appendix A
    /// pivot item (a)), same lifecycle as `set_cutoff_hz`. `false` (the
    /// default) keeps the base-rate path bit for bit (regression
    /// guarantee). The per-voice push is change-detected; an actual
    /// toggle swaps filter instances and resets the newly-active one —
    /// a brief click may be audible on sounding voices (documented on
    /// `Voice::set_oversample`).
    pub fn set_oversample(&mut self, on: bool) {
        self.oversample = on;
    }

    /// Set the GLOBAL LFO parameters (ROADMAP §1.1 GLOBAL block), same
    /// lifecycle as `set_cutoff_hz`. `wave` is the 0/1/2/3 wire
    /// encoding (triangle/saw/square/S&H — invalid values fall back to
    /// triangle; the FFI setter rejects them upstream, mirroring
    /// `set_osc`). `rate_hz` clamps to [0.05, 20], `to_pitch_cents` to
    /// [0, 100], `to_cutoff_oct` to [0, 2], `to_amp` to [0, 1]. All
    /// three depths default to 0 — every modulation factor is then
    /// exactly 1.0 and the render is bit-identical to the LFO-free
    /// engine (regression guarantee). The pitch depth heard is
    /// additionally scaled by the mod wheel (see the module docs).
    pub fn set_lfo(
        &mut self,
        wave: u32,
        rate_hz: f32,
        to_pitch_cents: f32,
        to_cutoff_oct: f32,
        to_amp: f32,
    ) {
        self.lfo
            .set_wave(LfoWave::from_u32(wave).unwrap_or(LfoWave::Triangle));
        self.lfo.set_rate_hz(rate_hz);
        self.lfo_to_pitch_cents = to_pitch_cents.clamp(0.0, 100.0);
        self.lfo_to_cutoff_oct = to_cutoff_oct.clamp(0.0, 2.0);
        self.lfo_to_amp = to_amp.clamp(0.0, 1.0);
    }

    /// Set the voice-allocation mode (wire encoding 0 = mono_legato,
    /// 1 = mono_retrig, 2 = poly — see [`VoiceMode::from_u32`]), same
    /// per-block push lifecycle as `set_cutoff_hz`. Change-detected, so
    /// re-pushing the current mode every block is free (and the default
    /// push is a no-op — regression guarantee). Invalid codes fall back
    /// to mono_legato (the FFI setter rejects them upstream, mirroring
    /// `set_osc` / `set_lfo`).
    ///
    /// ## Mode switches mid-performance (documented decision)
    ///
    /// Switching modes while notes sound RELEASES every voice and
    /// clears the held-notes stack. Migrating sounding voices into the
    /// new mode's topology has no clean answer in either direction
    /// (which of 8 poly voices becomes THE mono voice? does a held
    /// mono note fan out?), and a wrong answer means stuck notes — a
    /// sounding voice whose eventual note-off can no longer find it.
    /// Releasing everything keeps the invariant simple: keys already
    /// down fade out through their natural release tails, their later
    /// note-offs are harmless no-ops (mono only touches voice 0; poly
    /// matches by note and re-releasing a releasing voice is safe), and
    /// new presses allocate under the new mode against a consistent
    /// (empty) held-notes stack.
    pub fn set_voice_mode(&mut self, mode: u32) {
        let mode = VoiceMode::from_u32(mode).unwrap_or(VoiceMode::MonoLegato);
        if mode == self.voice_mode {
            return;
        }
        self.voice_mode = mode;
        for voice in &mut self.voices {
            // No-op on idle voices (`Adsr::note_off` guards on stage).
            voice.note_off();
        }
        self.held_notes.clear();
    }

    /// Set the pitch-bend range in semitones at full deflection, same
    /// lifecycle as `set_cutoff_hz`. Clamped to [0, 12]; default 2.0
    /// (the MIDI convention). Change-detected so the cached bend factor
    /// only recomputes when the knob (or a bend event) moves; at rest
    /// (bend center) the factor is exactly 1.0 — bit-transparent.
    pub fn set_bend_range(&mut self, semitones: f32) {
        let semitones = semitones.clamp(0.0, 12.0);
        if semitones != self.bend_range_semitones {
            self.bend_range_semitones = semitones;
            self.recompute_bend_factor();
        }
    }

    /// Recompute the cached pitch-bend frequency factor
    /// `2^(bend_range_semitones * bend_norm / 12)`. `exp2(0.0)` is
    /// exactly 1.0, so a centered bend (or a 0-semitone range) keeps
    /// the pitch path bit-transparent.
    fn recompute_bend_factor(&mut self) {
        self.bend_factor = (self.bend_range_semitones * self.bend_norm / 12.0).exp2();
    }

    /// Push a MIDI event into the synth. NoteOn/NoteOff drive the voice
    /// allocator; CC 1 (mod wheel) scales the LFO → pitch depth live
    /// (see the module docs for the wheel × depth decision); PitchBend
    /// updates the global bend factor. Other CCs are accepted silently
    /// (knob events go through the DSP atomics, not the MIDI queue).
    pub fn handle_event(&mut self, event: &MidiEvent) {
        match *event {
            MidiEvent::NoteOn { note, velocity, .. } => {
                if velocity == 0 {
                    // Per MIDI spec, NoteOn with velocity 0 == NoteOff.
                    self.note_off(note);
                } else {
                    self.note_on(note, velocity);
                }
            }
            MidiEvent::NoteOff { note, .. } => self.note_off(note),
            MidiEvent::ControlChange {
                controller: 1,
                value,
                ..
            } => {
                // Mod wheel: live 0..1 scale on the vibrato depth. This
                // REPLACES the boot value 1.0 — a wheel parked at 0
                // silences a configured vibrato depth from the first
                // event on (classic vibrato-on-wheel).
                self.mod_wheel = f32::from(value.min(127)) / 127.0;
            }
            MidiEvent::ControlChange { .. } => {
                // Other CCs: silently dropped until the mod matrix
                // grows more destinations.
            }
            MidiEvent::PitchBend { bend, .. } => {
                // 14-bit wire value, 8192 = center. Normalize to
                // [-1, 1]; clamp defends against out-of-range u16s
                // (the wire format is 0..=16383).
                self.bend_norm = ((f32::from(bend) - 8192.0) / 8192.0).clamp(-1.0, 1.0);
                self.recompute_bend_factor();
            }
        }
    }

    fn note_on(&mut self, note: u8, velocity: u8) {
        // MonoLegato semantics depend on "was another key already held
        // when this note arrived?" — so check the held-notes stack
        // BEFORE we push the new note onto it.
        let other_key_held = !self.held_notes.is_empty();
        self.held_notes.retain(|&n| n != note);
        self.held_notes.push(note);

        self.next_fire_id += 1;
        let fire_id = self.next_fire_id;
        // Mono modes always fire voice 0; poly picks free-or-stolen.
        let idx = match self.voice_mode {
            VoiceMode::MonoLegato | VoiceMode::MonoRetrig => 0,
            VoiceMode::Poly => self.poly_voice_index(),
        };
        // The velocity-routing amounts feed state CAPTURED at note fire
        // time (`Voice::set_velocity`). Push the freshest knob values
        // into the voice being fired BEFORE the capture — the per-block
        // render push may not have run yet for a note arriving in the
        // same block as a knob turn.
        self.voices[idx].set_vel_routing(self.vel_to_cutoff, self.vel_to_amp);
        match self.voice_mode {
            VoiceMode::MonoLegato | VoiceMode::MonoRetrig => {
                // MonoLegato suppresses envelope retrigger only when
                // another key is already held — a new note arriving
                // during release (no keys held) DOES retrigger the
                // envelope. MonoRetrig NEVER suppresses: every note-on
                // re-fires both envelopes (the attack re-ramps from the
                // envelope's current value — see `Adsr::note_on`).
                let suppress_retrigger = self.voice_mode == VoiceMode::MonoLegato && other_key_held;
                self.voices[0].note = Some(note);
                self.voices[0].set_velocity(velocity);
                self.voices[0].fired_at = fire_id;
                if !suppress_retrigger {
                    // Re-call note_on to retrigger envelopes from scratch.
                    self.voices[0].note_on(note, velocity, fire_id);
                }
            }
            VoiceMode::Poly => {
                self.voices[idx].note_on(note, velocity, fire_id);
            }
        }
    }

    /// Poly allocation (§1.2 / §1.5): the first free voice (amp env
    /// idle), else steal the sounding voice with the LOWEST `fired_at`
    /// — the "oldest" v1 policy. A possible refinement (left open, per
    /// ROADMAP §1.5's "oldest *released* voice first, fall back to
    /// oldest *playing*"): prefer the oldest voice already in its
    /// release tail — "oldest-quietest" — so a fading tail is cut
    /// before a held note. `fired_at` already carries everything that
    /// policy needs.
    fn poly_voice_index(&self) -> usize {
        if let Some(idx) = self.voices.iter().position(|v| !v.is_active()) {
            return idx;
        }
        self.voices
            .iter()
            .enumerate()
            .min_by_key(|(_, v)| v.fired_at)
            .map(|(idx, _)| idx)
            .expect("voice pool is never empty (MAX_VOICES > 0)")
    }

    fn note_off(&mut self, note: u8) {
        self.held_notes.retain(|&n| n != note);
        match self.voice_mode {
            VoiceMode::MonoLegato | VoiceMode::MonoRetrig => {
                if self.voices[0].note == Some(note) {
                    if let Some(&prev) = self.held_notes.last() {
                        // Fall back to the most recently-held remaining
                        // note (last-note priority). Don't retrigger
                        // envelopes — the fallback is a legato hand-off
                        // in BOTH mono modes (mono_retrig's "always
                        // retrigger" applies to note-on key presses,
                        // not to release fallbacks).
                        self.voices[0].note = Some(prev);
                    } else {
                        // No keys held — release the voice.
                        self.voices[0].note_off();
                    }
                }
            }
            VoiceMode::Poly => {
                // Release exactly the voice(s) sounding this note.
                // Plural: a retrigger during a release tail allocates a
                // second voice for the same note (the tail voice isn't
                // free), and one note-off gates them both. Re-releasing
                // an already-releasing voice is safe — `Adsr::note_off`
                // just re-captures the current value.
                for voice in &mut self.voices {
                    if voice.note == Some(note) {
                        voice.note_off();
                    }
                }
            }
        }
    }

    /// Render `samples` (interleaved stereo, length divisible by 2).
    /// Called from the audio thread.
    pub fn render(&mut self, samples: &mut [f32]) {
        // Apply the per-block parameter updates once. Per-voice
        // change-detection guards against redundant Moog retunes.
        for voice in &mut self.voices {
            voice.set_filter(self.cutoff_hz, self.resonance);
            voice.set_amp_env(self.amp_env);
            voice.set_filter_env(self.filter_env);
            for (idx, params) in self.osc_params.iter().enumerate() {
                voice.set_osc(idx, *params);
            }
            voice.set_noise_level(self.noise_level);
            voice.set_glide(self.glide_s);
            voice.set_pulse_width(self.pulse_width);
            voice.set_drive(self.drive);
            voice.set_vel_routing(self.vel_to_cutoff, self.vel_to_amp);
            voice.set_kbd_track(self.kbd_track);
            voice.set_oversample(self.oversample);
        }

        // Effective vibrato depth in cents: mod wheel × configured
        // depth (module docs). Constant within the block — CC events
        // are drained before render.
        let pitch_cents = self.mod_wheel * self.lfo_to_pitch_cents;

        let n_frames = samples.len() / 2;
        for frame in 0..n_frames {
            // Advance the GLOBAL LFO exactly once per sample. It
            // free-runs even while every depth is 0 (phase continuity —
            // engaging a depth mid-performance doesn't jump), which is
            // output-transparent because every factor below hard-skips
            // to exactly 1.0 while its depth is 0.
            let lfo = self.lfo.tick();
            // Vibrato: freq × 2^(lfo * cents / 1200).
            let vibrato_mul = if pitch_cents > 0.0 {
                (lfo * pitch_cents * (1.0 / 1200.0)).exp2()
            } else {
                1.0
            };
            // Bend factor is cached (1.0 at rest); ×1.0 is bit-exact.
            let pitch_mul = self.bend_factor * vibrato_mul;
            // Cutoff wobble: effective cutoff × 2^(lfo * oct), composed
            // into the canonical formula inside Voice::tick.
            let cutoff_mul = if self.lfo_to_cutoff_oct > 0.0 {
                (lfo * self.lfo_to_cutoff_oct).exp2()
            } else {
                1.0
            };
            // Tremolo: output × (1 - depth * (lfo*0.5 + 0.5)) — a
            // unipolar dip from unity (silent at the LFO peak when
            // depth = 1).
            let amp_mul = if self.lfo_to_amp > 0.0 {
                1.0 - self.lfo_to_amp * (lfo * 0.5 + 0.5)
            } else {
                1.0
            };
            // Sum all voices. Most will be idle and return 0.0 cheaply.
            let mut mono: f32 = 0.0;
            for voice in &mut self.voices {
                mono += voice.tick(pitch_mul, cutoff_mul);
            }
            // Voice output is unscaled (osc * env * vel). The post-synth
            // DSP chain handles patch gain / limiter — but a bare saw
            // peaks at ~±1, which is too hot for the limiter to clean
            // up. Scale by 0.5 here to land in the same ballpark as the
            // soundfont and plugin backends. Tremolo applies to the sum
            // (equivalent to per-voice VCA modulation for a global LFO,
            // ROADMAP §1.1 "LFO amp mod"); ×1.0 is bit-exact when off.
            let stereo = mono * 0.5 * amp_mul;
            samples[frame * 2] = stereo;
            samples[frame * 2 + 1] = stereo;
        }
    }

    /// Currently-loaded engine name — useful for logs and the
    /// `SynthBackend::name()` machinery. Held for forward compatibility
    /// once Phase 2 adds multiple engines.
    #[allow(dead_code)]
    pub fn engine_name(&self) -> &'static str {
        match self.engine {
            Engine::Minimoog => "minimoog",
        }
    }

    /// Sample rate this synth was built for. Held for future per-block
    /// param recompute (envelope retune on sample-rate change, etc.).
    #[allow(dead_code)]
    pub fn sample_rate(&self) -> f32 {
        self.sample_rate
    }

    /// Per-voice sounding notes, pool order (test probe for the poly
    /// allocator / steal policy).
    #[cfg(test)]
    pub(crate) fn voice_notes(&self) -> Vec<Option<u8>> {
        self.voices.iter().map(|v| v.note).collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Press `note` at velocity 100.
    fn press(synth: &mut NativeSynth, note: u8) {
        press_vel(synth, note, 100);
    }

    /// Press `note` at `velocity`.
    fn press_vel(synth: &mut NativeSynth, note: u8, velocity: u8) {
        synth.handle_event(&MidiEvent::NoteOn {
            channel: 0,
            note,
            velocity,
        });
    }

    /// Release `note`.
    fn release(synth: &mut NativeSynth, note: u8) {
        synth.handle_event(&MidiEvent::NoteOff { channel: 0, note });
    }

    /// Render `ms` milliseconds at 48 kHz in 128-frame blocks (matching
    /// the audio thread's chunked rendering), returning the interleaved
    /// stereo buffer. Callers gate notes via `press`/`release`.
    fn render_ms(synth: &mut NativeSynth, ms: usize) -> Vec<f32> {
        let mut samples = vec![0.0f32; 48 * ms * 2];
        for chunk in samples.chunks_mut(256) {
            synth.render(chunk);
        }
        samples
    }

    /// Render `ms` milliseconds of middle C at velocity 100, returning
    /// the interleaved stereo buffer.
    fn render_note_ms(synth: &mut NativeSynth, ms: usize) -> Vec<f32> {
        press(synth, 60);
        render_ms(synth, ms)
    }

    /// The 100 ms render used by the golden regression tests.
    fn render_note(synth: &mut NativeSynth) -> Vec<f32> {
        render_note_ms(synth, 100)
    }

    fn rms(samples: &[f32]) -> f32 {
        (samples.iter().map(|s| s * s).sum::<f32>() / samples.len() as f32).sqrt()
    }

    /// Extract the left channel from an interleaved stereo buffer (both
    /// channels are identical for the mono-per-voice engine).
    fn mono(samples: &[f32]) -> Vec<f32> {
        samples.iter().step_by(2).copied().collect()
    }

    /// Brightness proxy: normalized first-difference energy
    /// (sum (x[n+1]-x[n])^2 / sum x[n]^2). A first difference is a
    /// high-frequency emphasis, so a brighter (more open filter) signal
    /// scores higher regardless of its absolute amplitude.
    fn hf_ratio(mono_samples: &[f32]) -> f32 {
        let mut diff = 0.0f32;
        let mut total = 0.0f32;
        for w in mono_samples.windows(2) {
            let d = w[1] - w[0];
            diff += d * d;
            total += w[0] * w[0];
        }
        diff / total.max(1.0e-12)
    }

    /// REGRESSION GUARANTEE: with the resonance atomic left at its
    /// default (0.3), the rendered audio is bit-identical to the
    /// pre-`set_resonance` code. The golden bit patterns below were
    /// captured from the tree BEFORE this feature landed (same render:
    /// minimoog @ 48 kHz, note 60 vel 100, 100 ms in 128-frame blocks).
    #[test]
    fn default_render_matches_pre_change_golden() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let samples = render_note(&mut synth);

        // Non-silence + envelope contour: near-zero at the very first
        // sample (attack starts from 0), audibly loud overall.
        assert!(
            samples[1].abs() < 1e-3,
            "attack should start near zero, got {}",
            samples[1]
        );
        let r = rms(&samples);
        assert!(r > 0.05, "expected audible render, got rms={r}");

        // Bit-exact golden comparison (pre-change values).
        const GOLDEN: &[(usize, u32)] = &[
            (1, 0x353d0526),    // 7.041548e-7
            (960, 0x3d201cc5),  // 0.039089937
            (2400, 0x3b6dd2dc), // 0.0036289012
            (4800, 0xbe6bb076), // -0.23016533
            (9599, 0xbe121554), // -0.14265949
        ];
        for &(idx, bits) in GOLDEN {
            assert_eq!(
                samples[idx].to_bits(),
                bits,
                "sample[{idx}] regressed: got {} ({:#010x}), want {:#010x}",
                samples[idx],
                samples[idx].to_bits(),
                bits
            );
        }
        assert_eq!(
            r.to_bits(),
            0x3dedb728, // 0.116072
            "rms regressed: got {r} ({:#010x})",
            r.to_bits()
        );
    }

    /// Turning resonance up to 0.9 must audibly change the rendered
    /// audio versus the 0.3 default.
    #[test]
    fn resonance_change_alters_render() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut hot = NativeSynth::new("minimoog", 48_000.0).unwrap();
        hot.set_resonance(0.9);

        let a = render_note(&mut base);
        let b = render_note(&mut hot);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        let max_diff = a
            .iter()
            .zip(&b)
            .map(|(x, y)| (x - y).abs())
            .fold(0.0f32, f32::max);
        assert!(
            max_diff > 0.01,
            "resonance 0.9 vs 0.3 should change the output, max sample diff={max_diff}"
        );
        let rms_diff = (rms(&a) - rms(&b)).abs();
        assert!(
            rms_diff > 1e-4,
            "expected RMS to shift with resonance, diff={rms_diff}"
        );
    }

    /// `set_resonance` clamps to [0.0, 0.95] — headroom below the
    /// Stilson/Smith ladder's self-oscillation instability.
    #[test]
    fn set_resonance_clamps() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_resonance(2.0);
        assert_eq!(synth.resonance, 0.95);
        synth.set_resonance(-1.0);
        assert_eq!(synth.resonance, 0.0);
        synth.set_resonance(0.5);
        assert_eq!(synth.resonance, 0.5);
    }

    /// REGRESSION GUARANTEE (stage 2): env-2 with amount 0 is
    /// bit-transparent. Even with non-default ADSR times pushed, the
    /// render is bit-identical to an untouched synth — proving the
    /// per-sample modulation path adds nothing when the amount knob is
    /// off. (The untouched-default path is separately pinned to the
    /// pre-change golden bits by `default_render_matches_pre_change_golden`.)
    #[test]
    fn filter_env_amount_zero_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut modded = NativeSynth::new("minimoog", 48_000.0).unwrap();
        // Wild ADSR times, but amount = 0 — must not change a single bit.
        modded.set_filter_env(0.05, 0.1, 0.9, 1.0, 0.0);

        let a = render_note(&mut base);
        let b = render_note(&mut modded);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged with filter-env amount=0: {x} vs {y}"
            );
        }
    }

    /// Turning the env-2 amount up to 1.0 must audibly change the render
    /// versus the default (amount 0), and the note attack must get
    /// brighter (the envelope sweeps the cutoff up to +4 octaves).
    #[test]
    fn filter_env_amount_one_changes_attack() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut swept = NativeSynth::new("minimoog", 48_000.0).unwrap();
        swept.set_filter_env(0.005, 0.600, 0.4, 0.600, 1.0);

        let a = render_note(&mut base);
        let b = render_note(&mut swept);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        let max_diff = a
            .iter()
            .zip(&b)
            .map(|(x, y)| (x - y).abs())
            .fold(0.0f32, f32::max);
        assert!(
            max_diff > 0.01,
            "filter-env amount 1.0 should change the output, max sample diff={max_diff}"
        );
        let rms_diff = (rms(&a) - rms(&b)).abs();
        assert!(
            rms_diff > 1e-4,
            "expected RMS to shift with the filter env, diff={rms_diff}"
        );

        // Spectral check over the attack window (10..100 ms): the swept
        // render must be brighter than the unswept one.
        let a_attack = mono(&a[960..]);
        let b_attack = mono(&b[960..]);
        let (ra, rb) = (hf_ratio(&a_attack), hf_ratio(&b_attack));
        assert!(
            rb > ra * 1.2,
            "swept attack should be brighter: hf_ratio swept={rb} vs base={ra}"
        );
    }

    /// Brightness contour over a long note: with amount = 1 the note
    /// starts bright (env at peak, cutoff swept up) and darkens as the
    /// envelope decays to sustain — measured via the normalized
    /// first-difference energy of an early vs a late window.
    #[test]
    fn filter_env_brightness_decays_over_note() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        // Lower the base cutoff so the sweep dominates the spectrum.
        synth.set_cutoff_hz(500.0);
        synth.set_filter_env(0.005, 0.600, 0.4, 0.600, 1.0);

        let samples = render_note_ms(&mut synth, 900);
        let m = mono(&samples);
        // Early window 10..110 ms: env near peak (effective ≈ 8 kHz).
        let early = &m[480..5280];
        // Late window 700..800 ms: env at sustain 0.4 (effective ≈ 1.5 kHz).
        let late = &m[33_600..38_400];
        assert!(
            rms(early) > 0.01 && rms(late) > 0.01,
            "both windows audible"
        );

        let (re, rl) = (hf_ratio(early), hf_ratio(late));
        assert!(
            re > rl * 1.5,
            "note should darken as env 2 decays: early hf_ratio={re}, late hf_ratio={rl}"
        );
    }

    /// Envelope contour on the modulated cutoff itself, probed via the
    /// voice's applied-cutoff accessor: peak (clamped at 20 kHz) after
    /// the attack, partway down mid-decay, settled at
    /// base * 2^(amount * sustain * 4) ≈ 6063 Hz in sustain, and lower
    /// again during release.
    #[test]
    fn filter_env_cutoff_contour() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_filter_env(0.005, 0.600, 0.4, 0.600, 1.0);
        synth.handle_event(&MidiEvent::NoteOn {
            channel: 0,
            note: 60,
            velocity: 100,
        });

        let mut buf = [0.0f32; 256]; // 128 frames ≈ 2.67 ms per block
        let mut render_blocks = |synth: &mut NativeSynth, n: usize| {
            for _ in 0..n {
                synth.render(&mut buf);
            }
        };

        // ~10.7 ms: attack (5 ms) complete, env ≈ 1.0 →
        // effective = 2000 * 2^4 = 32 kHz, clamped to 20 kHz.
        render_blocks(&mut synth, 4);
        let peak = synth.voices[0].applied_cutoff_hz();
        assert!(
            peak > 19_000.0,
            "post-attack cutoff should clamp near 20 kHz, got {peak}"
        );

        // ~299 ms: mid-decay, env ≈ 0.71 → effective ≈ 14.2 kHz.
        render_blocks(&mut synth, 108);
        let mid = synth.voices[0].applied_cutoff_hz();
        assert!(
            mid < peak - 1_000.0 && mid > 8_000.0,
            "mid-decay cutoff should sit between peak and sustain, got {mid}"
        );

        // ~700 ms: decay (600 ms) done, env = sustain 0.4 →
        // effective = 2000 * 2^1.6 ≈ 6062.9 Hz.
        render_blocks(&mut synth, 151);
        let sus = synth.voices[0].applied_cutoff_hz();
        let expected = 2_000.0 * (1.6f32).exp2();
        assert!(
            (sus - expected).abs() < 5.0,
            "sustain cutoff should be ≈{expected}, got {sus}"
        );

        // Release: ~300 ms in (of 600 ms), env ≈ 0.2 → effective ≈ 3.5 kHz.
        synth.handle_event(&MidiEvent::NoteOff {
            channel: 0,
            note: 60,
        });
        render_blocks(&mut synth, 112);
        let rel = synth.voices[0].applied_cutoff_hz();
        assert!(
            rel < sus - 1_000.0 && rel > 2_500.0,
            "mid-release cutoff should fall below sustain, got {rel}"
        );
    }

    /// REGRESSION (env-2 stale-state): with the amp release shorter
    /// than the filter release (the defaults: 0.4 s < 0.6 s; here the
    /// gap is widened to 2 s), the voice goes idle mid-filter-release
    /// and env2 used to freeze at a nonzero value. That stale value —
    /// plus the stale applied-cutoff cache behind the >0.5 Hz retune
    /// hysteresis — then colored the next note's attack. After the fix
    /// (hard reset of env2 + applied-cutoff at the idle transition),
    /// the second note's applied-cutoff contour must match a fresh
    /// voice's bit for bit, starting from the unmodulated base.
    #[test]
    fn filter_env_resets_when_voice_goes_idle() {
        let mut used = NativeSynth::new("minimoog", 48_000.0).unwrap();
        used.set_filter_env(0.005, 0.600, 0.4, 2.0, 1.0);

        // First note: sound it, release it, render past the 400 ms amp
        // release (well inside the 2 s filter release) to full idle.
        press(&mut used, 60);
        render_ms(&mut used, 300);
        release(&mut used, 60);
        let tail = render_ms(&mut used, 600);
        assert!(
            tail[500 * 96..].iter().all(|&s| s == 0.0),
            "voice should be fully silent before the second note"
        );
        assert!(!used.voices[0].is_active(), "voice should be idle");
        let idle_cutoff = used.voices[0].applied_cutoff_hz();
        assert_eq!(
            idle_cutoff.to_bits(),
            2_000.0f32.to_bits(),
            "idle transition must drop the applied cutoff back to the \
             unmodulated base, got {idle_cutoff}"
        );

        // Second note vs a fresh voice with identical params: the
        // applied-cutoff attack contour must be bit-identical. Before
        // the fix the reused voice's env2 restarted from the frozen
        // mid-release value (≈0.32 ⇒ cutoff ≈4.9 kHz instead of 2 kHz)
        // and diverged from the first block on.
        let mut fresh = NativeSynth::new("minimoog", 48_000.0).unwrap();
        fresh.set_filter_env(0.005, 0.600, 0.4, 2.0, 1.0);
        press(&mut used, 60);
        press(&mut fresh, 60);
        let mut buf_used = [0.0f32; 256];
        let mut buf_fresh = [0.0f32; 256];
        for block in 0..80 {
            used.render(&mut buf_used);
            fresh.render(&mut buf_fresh);
            let (cu, cf) = (
                used.voices[0].applied_cutoff_hz(),
                fresh.voices[0].applied_cutoff_hz(),
            );
            assert_eq!(
                cu.to_bits(),
                cf.to_bits(),
                "block {block}: reused voice's applied cutoff diverged from a \
                 fresh voice's contour ({cu} vs {cf})"
            );
        }
    }

    /// Peak absolute sample value.
    fn peak(samples: &[f32]) -> f32 {
        samples.iter().fold(0.0f32, |m, s| m.max(s.abs()))
    }

    /// Rising zero crossings of the left channel over the sustain
    /// window (skipping `skip` interleaved samples of attack/decay) —
    /// a fundamental-frequency proxy.
    fn rising_crossings(samples: &[f32], skip: usize) -> usize {
        let m = mono(&samples[skip..]);
        m.windows(2).filter(|w| w[0] < 0.0 && w[1] >= 0.0).count()
    }

    /// Rising zero crossings of the left channel between `from_ms` and
    /// `to_ms` (48 kHz interleaved buffer ⇒ 96 samples per ms) — the
    /// same fundamental-frequency proxy, windowed.
    fn rising_crossings_between(samples: &[f32], from_ms: usize, to_ms: usize) -> usize {
        let m = mono(&samples[from_ms * 96..to_ms * 96]);
        m.windows(2).filter(|w| w[0] < 0.0 && w[1] >= 0.0).count()
    }

    /// REGRESSION GUARANTEE (stage 3): explicitly pushing the default
    /// oscillator bank + noise level through the setters is
    /// bit-transparent — the render matches an untouched synth sample
    /// for sample. (The untouched-default path is separately pinned to
    /// the pre-change golden bits by
    /// `default_render_matches_pre_change_golden`, which now exercises
    /// the stage-3 mixer path at its defaults.)
    #[test]
    fn osc_defaults_push_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut pushed = NativeSynth::new("minimoog", 48_000.0).unwrap();
        pushed.set_osc(0, 0, 0, 0.0, 1.0);
        pushed.set_osc(1, 0, 0, -7.0, 0.0);
        pushed.set_osc(2, 0, -1, 5.0, 0.0);
        pushed.set_noise_level(0.0);

        let a = render_note(&mut base);
        let b = render_note(&mut pushed);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged after pushing default osc params: {x} vs {y}"
            );
        }
    }

    /// Turning osc 2's level up (with its default -7 cent detune) must
    /// audibly change the render versus the default single-osc mix.
    #[test]
    fn osc2_level_changes_render() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut thick = NativeSynth::new("minimoog", 48_000.0).unwrap();
        thick.set_osc(1, 0, 0, -7.0, 0.7);

        let a = render_note(&mut base);
        let b = render_note(&mut thick);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        let max_diff = a
            .iter()
            .zip(&b)
            .map(|(x, y)| (x - y).abs())
            .fold(0.0f32, f32::max);
        assert!(
            max_diff > 0.01,
            "osc2 level 0.7 should change the output, max sample diff={max_diff}"
        );
    }

    /// Octave -1 on osc 1 halves the fundamental, measured via rising
    /// zero-crossing counts over a post-attack window.
    #[test]
    fn octave_down_halves_fundamental() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut sub = NativeSynth::new("minimoog", 48_000.0).unwrap();
        sub.set_osc(0, 0, -1, 0.0, 1.0);

        let a = render_note_ms(&mut base, 500);
        let b = render_note_ms(&mut sub, 500);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        // Skip the first 100 ms (attack + decay transient), count over
        // the remaining 400 ms of sustain. Note 60 ≈ 261.6 Hz → ~104
        // rising crossings; octave -1 ≈ 130.8 Hz → ~52.
        let ca = rising_crossings(&a, 9600);
        let cb = rising_crossings(&b, 9600);
        let ratio = ca as f32 / cb as f32;
        assert!(
            (1.8..=2.2).contains(&ratio),
            "octave -1 should halve zero crossings: base={ca}, sub={cb}, ratio={ratio}"
        );
    }

    /// Switching osc 1 to square must change the render versus saw.
    #[test]
    fn square_differs_from_saw() {
        let mut saw = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut square = NativeSynth::new("minimoog", 48_000.0).unwrap();
        square.set_osc(0, 1, 0, 0.0, 1.0);

        let a = render_note(&mut saw);
        let b = render_note(&mut square);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        let max_diff = a
            .iter()
            .zip(&b)
            .map(|(x, y)| (x - y).abs())
            .fold(0.0f32, f32::max);
        assert!(
            max_diff > 0.1,
            "square vs saw should differ, max sample diff={max_diff}"
        );
    }

    /// Noise level > 0 adds broadband energy: the render changes and
    /// its high-frequency ratio rises versus the noise-free default.
    #[test]
    fn noise_adds_broadband_energy() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut noisy = NativeSynth::new("minimoog", 48_000.0).unwrap();
        noisy.set_noise_level(0.8);

        let a = render_note(&mut base);
        let b = render_note(&mut noisy);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        let max_diff = a
            .iter()
            .zip(&b)
            .map(|(x, y)| (x - y).abs())
            .fold(0.0f32, f32::max);
        assert!(
            max_diff > 0.01,
            "noise level 0.8 should change the output, max sample diff={max_diff}"
        );

        // Broadband check over the post-attack window: white noise is
        // spectrally flat up to the cutoff, so the noisy render must
        // score higher on the first-difference (HF) ratio.
        let (ra, rb) = (hf_ratio(&mono(&a[960..])), hf_ratio(&mono(&b[960..])));
        assert!(
            rb > ra * 1.2,
            "noise should add broadband energy: hf_ratio noisy={rb} vs base={ra}"
        );
    }

    /// Mixer renormalization: with every source cranked to 1.0 the
    /// voice divides by Σlevels = 4, keeping the peak below clipping
    /// and in the same ballpark as the single-osc default.
    #[test]
    fn mixer_renormalization_prevents_clipping() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut cranked = NativeSynth::new("minimoog", 48_000.0).unwrap();
        cranked.set_osc(0, 0, 0, 0.0, 1.0);
        cranked.set_osc(1, 0, 0, -7.0, 1.0);
        cranked.set_osc(2, 0, -1, 5.0, 1.0);
        cranked.set_noise_level(1.0);

        let a = render_note(&mut base);
        let b = render_note(&mut cranked);
        assert!(rms(&b) > 0.05, "cranked render audible");

        let (pa, pb) = (peak(&a), peak(&b));
        assert!(
            pb < 1.0,
            "all-sources-1 peak must stay below clipping, got {pb}"
        );
        assert!(
            pb < pa * 2.0,
            "renormalized full mix should stay near the single-osc level: cranked peak={pb}, base peak={pa}"
        );
    }

    /// `set_osc` clamps octave/detune/level, falls back to saw on an
    /// invalid wave code, and ignores out-of-range indices.
    #[test]
    fn set_osc_clamps_and_ignores_bad_idx() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_osc(1, 9, 5, 500.0, 3.0);
        assert_eq!(synth.osc_params[1].wave, Waveform::Saw);
        assert_eq!(synth.osc_params[1].octave, 2);
        assert_eq!(synth.osc_params[1].detune_cents, 100.0);
        assert_eq!(synth.osc_params[1].level, 1.0);

        let before = synth.osc_params;
        synth.set_osc(3, 1, 0, 0.0, 0.5); // out of range → ignored
        assert_eq!(synth.osc_params, before);

        synth.set_noise_level(2.0);
        assert_eq!(synth.noise_level, 1.0);
        synth.set_noise_level(-1.0);
        assert_eq!(synth.noise_level, 0.0);
    }

    /// `set_filter_env` clamps: times to [0.0001, 10] s, sustain and
    /// amount to [0, 1].
    #[test]
    fn set_filter_env_clamps() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_filter_env(-1.0, 100.0, 7.0, 0.0, 2.0);
        assert_eq!(synth.filter_env.attack_s, 1.0e-4);
        assert_eq!(synth.filter_env.decay_s, 10.0);
        assert_eq!(synth.filter_env.sustain, 1.0);
        assert_eq!(synth.filter_env.release_s, 1.0e-4);
        assert_eq!(synth.filter_env.amount, 1.0);
    }

    /// REGRESSION GUARANTEE (stage 4): explicitly pushing glide 0 (the
    /// default) is bit-transparent — the render matches an untouched
    /// synth sample for sample. (The untouched-default path is
    /// separately pinned to the pre-change golden bits by
    /// `default_render_matches_pre_change_golden`, which now exercises
    /// the stage-4 slew branch at its default.)
    #[test]
    fn glide_zero_push_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut pushed = NativeSynth::new("minimoog", 48_000.0).unwrap();
        pushed.set_glide(0.0);

        let a = render_note(&mut base);
        let b = render_note(&mut pushed);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged after pushing glide 0: {x} vs {y}"
            );
        }
    }

    /// A voice starting FROM SILENCE begins exactly at its target pitch
    /// — even with glide enabled, a single note from idle renders
    /// bit-identical to the glide-free engine (the slew has zero
    /// distance to cover). This pins the documented glide-reset policy
    /// (see `Voice::note_on`) at the strongest possible level.
    #[test]
    fn glide_single_note_from_silence_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut glided = NativeSynth::new("minimoog", 48_000.0).unwrap();
        glided.set_glide(0.2);

        let a = render_note(&mut base);
        let b = render_note(&mut glided);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged for a from-silence note with glide on: {x} vs {y}"
            );
        }
    }

    /// Legato glide: with a 0.2 s glide, pressing C4 while C3 is held
    /// sweeps the fundamental — mid-glide the zero-crossing rate sits
    /// strictly between the two notes' rates, and it settles at the
    /// target note's rate once the sweep completes.
    ///
    /// Expected counts (freq(t) = 261.63 − 130.81·e^(−t/0.2) Hz):
    /// C3 over 200 ms ≈ 26 crossings; C4 over 200 ms ≈ 52; the 50–150 ms
    /// mid-glide window integrates to ≈ 18 cycles vs 13 (pure C3) and
    /// 26 (pure C4).
    #[test]
    fn glide_legato_sweeps_fundamental() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_glide(0.2);

        press(&mut synth, 48); // C3 ≈ 130.81 Hz
        let settle = render_ms(&mut synth, 500);
        assert!(rms(&settle) > 0.05, "settle phase audible");
        let low = rising_crossings_between(&settle, 300, 500);
        assert!(
            (23..=29).contains(&low),
            "settled C3 rate should be ≈26 crossings/200 ms, got {low}"
        );

        press(&mut synth, 60); // C4 legato — C3 still held
        let glide = render_ms(&mut synth, 1500);
        assert!(rms(&glide) > 0.05, "glide phase audible");

        let mid = rising_crossings_between(&glide, 50, 150);
        assert!(
            (15..=22).contains(&mid),
            "mid-glide rate should sit between C3 (≈13/100 ms) and C4 (≈26/100 ms), got {mid}"
        );

        let end = rising_crossings_between(&glide, 1200, 1400);
        assert!(
            (49..=56).contains(&end),
            "post-glide rate should settle at C4 (≈52 crossings/200 ms), got {end}"
        );
    }

    /// Glide applies to RETRIGGERED notes too (Minimoog behavior): a new
    /// note pressed while the previous one is still in its release tail
    /// retriggers the envelopes but the pitch glides from the old note.
    #[test]
    fn glide_applies_on_retrigger() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_glide(0.2);

        press(&mut synth, 48);
        render_ms(&mut synth, 300);
        release(&mut synth, 48);
        // 50 ms into the 400 ms release — the voice is still sounding.
        render_ms(&mut synth, 50);

        press(&mut synth, 60); // no key held ⇒ envelopes retrigger
        let glide = render_ms(&mut synth, 400);
        assert!(rms(&glide) > 0.05, "retriggered note audible");
        let mid = rising_crossings_between(&glide, 50, 150);
        assert!(
            (15..=22).contains(&mid),
            "retriggered note should glide from C3 toward C4 (expected ≈18 crossings/100 ms, \
             between 13 and 26), got {mid}"
        );
    }

    /// Glide state resets when a voice starts from silence: after the
    /// previous phrase fully releases, the next note starts at its own
    /// pitch instead of gliding from the last note of the old phrase
    /// (documented choice — see `Voice::note_on`). With a 1 s glide, a
    /// leftover sweep from C5 would still read ≈450 Hz over the probe
    /// window; a correct reset reads C3's ≈131 Hz.
    #[test]
    fn glide_resets_when_voice_starts_from_silence() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_glide(1.0);

        press(&mut synth, 72); // C5 ≈ 523.25 Hz
        render_ms(&mut synth, 300);
        release(&mut synth, 72);
        // Render past the full 400 ms release; the voice goes idle.
        let tail = render_ms(&mut synth, 600);
        let silence = &tail[500 * 96..];
        assert!(
            silence.iter().all(|&s| s == 0.0),
            "voice should be fully silent before the new phrase"
        );

        press(&mut synth, 48); // C3 — a new phrase from silence
        let fresh = render_ms(&mut synth, 400);
        assert!(rms(&fresh) > 0.05, "new phrase audible");
        let count = rising_crossings_between(&fresh, 100, 300);
        assert!(
            (23..=30).contains(&count),
            "a from-silence note must start at its own pitch (C3 ≈ 26 crossings/200 ms); \
             a leftover C5→C3 glide would read ≈90. got {count}"
        );
    }

    /// `set_glide` clamps to [0, 5] seconds.
    #[test]
    fn set_glide_clamps() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_glide(9.0);
        assert_eq!(synth.glide_s, 5.0);
        synth.set_glide(-1.0);
        assert_eq!(synth.glide_s, 0.0);
        synth.set_glide(0.2);
        assert_eq!(synth.glide_s, 0.2);
    }

    /// REGRESSION GUARANTEE (stage 5): explicitly pushing the amp-env
    /// defaults (5 ms / 200 ms / 0.7 / 400 ms — the previously-hardcoded
    /// values) is bit-transparent — the render matches an untouched
    /// synth sample for sample. (The untouched-default path is
    /// separately pinned to the pre-change golden bits by
    /// `default_render_matches_pre_change_golden`, which now exercises
    /// the stage-5 amp-env push at its defaults.)
    #[test]
    fn amp_env_defaults_push_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut pushed = NativeSynth::new("minimoog", 48_000.0).unwrap();
        pushed.set_amp_env(0.005, 0.200, 0.7, 0.400);

        let a = render_note(&mut base);
        let b = render_note(&mut pushed);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged after pushing default amp env: {x} vs {y}"
            );
        }
    }

    /// Shortening the amp release from the default 400 ms to 50 ms
    /// measurably shortens the release tail: the short-release voice is
    /// exactly silent (voice idle => 0.0) well before the default-release
    /// voice has finished ramping down.
    #[test]
    fn amp_env_release_change_alters_tail_length() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut snappy = NativeSynth::new("minimoog", 48_000.0).unwrap();
        snappy.set_amp_env(0.005, 0.200, 0.7, 0.050);

        // Sound both, then release and render a 400 ms tail.
        press(&mut base, 60);
        press(&mut snappy, 60);
        render_ms(&mut base, 300);
        render_ms(&mut snappy, 300);
        release(&mut base, 60);
        release(&mut snappy, 60);
        let tail_a = render_ms(&mut base, 400);
        let tail_b = render_ms(&mut snappy, 400);

        // 150 ms in: the 50 ms release finished long ago — the snappy
        // voice went idle and outputs exact zeros...
        assert!(
            tail_b[150 * 96..].iter().all(|&s| s == 0.0),
            "50 ms release should be exactly silent 150 ms after note-off"
        );
        // ...while the default 400 ms release is still audibly ramping
        // over the same 150-350 ms window.
        let late_a = &tail_a[150 * 96..350 * 96];
        assert!(
            rms(late_a) > 1e-3,
            "400 ms release should still be audible 150-350 ms after note-off, rms={}",
            rms(late_a)
        );
    }

    /// `set_amp_env` clamps: times to [0.0001, 10] s, sustain to [0, 1].
    #[test]
    fn set_amp_env_clamps() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_amp_env(-1.0, 100.0, 7.0, 0.0);
        assert_eq!(synth.amp_env.attack_s, 1.0e-4);
        assert_eq!(synth.amp_env.decay_s, 10.0);
        assert_eq!(synth.amp_env.sustain, 1.0);
        assert_eq!(synth.amp_env.release_s, 1.0e-4);
    }

    /// REGRESSION GUARANTEE (stage 5): explicitly pushing the default
    /// pulse width (0.25 — the old fixed stage-3 duty) is
    /// bit-transparent even WITH a pulse oscillator active, proving the
    /// runtime width feeds `PolyPulse` exactly the value the removed
    /// constant did.
    #[test]
    fn pulse_width_default_push_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut pushed = NativeSynth::new("minimoog", 48_000.0).unwrap();
        base.set_osc(0, 2, 0, 0.0, 1.0); // osc 1 -> pulse
        pushed.set_osc(0, 2, 0, 0.0, 1.0);
        pushed.set_pulse_width(0.25);

        let a = render_note(&mut base);
        let b = render_note(&mut pushed);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged after pushing default pulse width: {x} vs {y}"
            );
        }
    }

    /// Pulse width 0.5 vs the 0.25 default must audibly change the
    /// render while a pulse oscillator is active.
    #[test]
    fn pulse_width_half_differs_from_quarter() {
        let mut quarter = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut half = NativeSynth::new("minimoog", 48_000.0).unwrap();
        quarter.set_osc(0, 2, 0, 0.0, 1.0); // osc 1 -> pulse
        half.set_osc(0, 2, 0, 0.0, 1.0);
        half.set_pulse_width(0.5);

        let a = render_note(&mut quarter);
        let b = render_note(&mut half);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        let max_diff = a
            .iter()
            .zip(&b)
            .map(|(x, y)| (x - y).abs())
            .fold(0.0f32, f32::max);
        assert!(
            max_diff > 0.1,
            "pulse width 0.5 vs 0.25 should change the output, max sample diff={max_diff}"
        );
    }

    /// `set_pulse_width` clamps to [0.05, 0.95].
    #[test]
    fn set_pulse_width_clamps() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_pulse_width(2.0);
        assert_eq!(synth.pulse_width, 0.95);
        synth.set_pulse_width(-1.0);
        assert_eq!(synth.pulse_width, 0.05);
        synth.set_pulse_width(0.5);
        assert_eq!(synth.pulse_width, 0.5);
    }

    /// REGRESSION GUARANTEE (stage 5): drive 0 is a bit-exact bypass —
    /// explicitly pushing 0.0 renders identical to an untouched synth
    /// sample for sample. (The untouched-default path is separately
    /// pinned to the pre-change golden bits by
    /// `default_render_matches_pre_change_golden`, which now exercises
    /// the stage-5 drive branch at its default.)
    #[test]
    fn drive_zero_push_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut pushed = NativeSynth::new("minimoog", 48_000.0).unwrap();
        pushed.set_drive(0.0);

        let a = render_note(&mut base);
        let b = render_note(&mut pushed);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged after pushing drive 0: {x} vs {y}"
            );
        }
    }

    /// Drive 1 saturates the pre-filter signal: the waveshape changes
    /// beyond a pure gain (i.e. the harmonic structure moved), the
    /// waveform gets denser (RMS rises relative to peak — the tanh
    /// squashes peaks while small signals keep unity gain), and the
    /// zero-crossing rate stays put (harmonic distortion, not a pitch
    /// change). The filter is opened to 20 kHz so the ladder doesn't
    /// smear the shape being measured. A first-difference (hf_ratio)
    /// check is deliberately NOT used here: squaring up a saw *lowers*
    /// that ratio even as the tone gets audibly grittier.
    #[test]
    fn drive_one_saturates_waveform() {
        let mut clean = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut driven = NativeSynth::new("minimoog", 48_000.0).unwrap();
        clean.set_cutoff_hz(20_000.0);
        driven.set_cutoff_hz(20_000.0);
        driven.set_drive(1.0);

        let a = render_note_ms(&mut clean, 500);
        let b = render_note_ms(&mut driven, 500);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.01, "both renders audible");

        // Post-attack mono window (skip the first 100 ms).
        let ma = mono(&a[9600..]);
        let mb = mono(&b[9600..]);

        // 1. Nonlinear waveshaping: after normalizing both windows to
        //    unit RMS, the shapes still differ substantially — drive is
        //    not just a volume change.
        let (ra, rb) = (rms(&ma), rms(&mb));
        let shape_diff = (ma
            .iter()
            .zip(&mb)
            .map(|(x, y)| {
                let d = x / ra - y / rb;
                d * d
            })
            .sum::<f32>()
            / ma.len() as f32)
            .sqrt();
        assert!(
            shape_diff > 0.1,
            "drive 1 should reshape the waveform, unit-RMS shape diff={shape_diff}"
        );

        // 2. Energy heuristic: saturation packs more energy per unit
        //    peak (saw crest ~0.44 here; tanh-squared-up ~0.58).
        let (crest_a, crest_b) = (ra / peak(&ma), rb / peak(&mb));
        assert!(
            crest_b > crest_a * 1.2,
            "drive 1 should raise RMS relative to peak: driven crest={crest_b} vs clean={crest_a}"
        );

        // 3. Same fundamental: rising zero-crossing counts match within
        //    a couple of cycles.
        let (za, zb) = (rising_crossings(&a, 9600), rising_crossings(&b, 9600));
        assert!(
            (za as i32 - zb as i32).abs() <= 2,
            "drive must not change the pitch: clean={za} vs driven={zb} rising crossings"
        );
    }

    /// `set_drive` clamps to [0, 1].
    #[test]
    fn set_drive_clamps() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_drive(2.0);
        assert_eq!(synth.drive, 1.0);
        synth.set_drive(-1.0);
        assert_eq!(synth.drive, 0.0);
        synth.set_drive(0.3);
        assert_eq!(synth.drive, 0.3);
    }

    /// REGRESSION GUARANTEE (velocity routing + kbd tracking):
    /// explicitly pushing the defaults (vel→cutoff 0, vel→amp 1,
    /// kbd-track 0) is bit-transparent — the render matches an
    /// untouched synth sample for sample. (The untouched-default path
    /// is separately pinned to the pre-change golden bits by
    /// `default_render_matches_pre_change_golden`, which now exercises
    /// the routing branches at their defaults.)
    #[test]
    fn vel_routing_defaults_push_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut pushed = NativeSynth::new("minimoog", 48_000.0).unwrap();
        pushed.set_vel_routing(0.0, 1.0);
        pushed.set_kbd_track(0.0);

        let a = render_note(&mut base);
        let b = render_note(&mut pushed);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged after pushing default vel routing: {x} vs {y}"
            );
        }
    }

    /// At the default vel→amp = 1 the classic `velocity / 127` scaling
    /// is live: velocity 120 renders substantially louder than
    /// velocity 20.
    #[test]
    fn velocity_scales_amplitude_at_default() {
        let mut soft = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut hard = NativeSynth::new("minimoog", 48_000.0).unwrap();
        press_vel(&mut soft, 60, 20);
        press_vel(&mut hard, 60, 120);
        let a = render_ms(&mut soft, 100);
        let b = render_ms(&mut hard, 100);
        assert!(rms(&b) > 0.05, "vel 120 render audible");
        assert!(
            rms(&b) > rms(&a) * 3.0,
            "vel 120 should be much louder than vel 20 at default routing: \
             rms(120)={}, rms(20)={}",
            rms(&b),
            rms(&a)
        );
    }

    /// vel→amp = 0 ignores velocity entirely: velocity 20 and
    /// velocity 120 render bit-identically (both at full amplitude).
    #[test]
    fn vel_to_amp_zero_ignores_velocity() {
        let mut soft = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut hard = NativeSynth::new("minimoog", 48_000.0).unwrap();
        soft.set_vel_routing(0.0, 0.0);
        hard.set_vel_routing(0.0, 0.0);

        press_vel(&mut soft, 60, 20);
        press_vel(&mut hard, 60, 120);
        let a = render_ms(&mut soft, 100);
        let b = render_ms(&mut hard, 100);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged between vel 20 and vel 120 with vel_to_amp=0: {x} vs {y}"
            );
        }
    }

    /// vel→cutoff = 1 opens the filter for hard hits: velocity 120
    /// renders brighter than velocity 20 (hf-energy heuristic).
    /// vel→amp is set to 0 so both notes sound at the same amplitude —
    /// only the filter differs.
    #[test]
    fn vel_to_cutoff_one_brightens_high_velocity() {
        let mut soft = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut hard = NativeSynth::new("minimoog", 48_000.0).unwrap();
        soft.set_vel_routing(1.0, 0.0);
        hard.set_vel_routing(1.0, 0.0);

        press_vel(&mut soft, 60, 20);
        press_vel(&mut hard, 60, 120);
        let a = render_ms(&mut soft, 100);
        let b = render_ms(&mut hard, 100);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        // Post-attack window (skip 10 ms): vel 120 sits at cutoff
        // 2000 * 2^((120/127-0.5)*2) ≈ 3706 Hz, vel 20 at ≈ 1244 Hz.
        let (ra, rb) = (hf_ratio(&mono(&a[960..])), hf_ratio(&mono(&b[960..])));
        assert!(
            rb > ra * 1.2,
            "vel 120 should be brighter than vel 20 with vel_to_cutoff=1: \
             hf_ratio hard={rb} vs soft={ra}"
        );
    }

    /// The vel→cutoff multiplier is CAPTURED at note_on: turning the
    /// knob mid-note leaves the sounding note untouched; the next note
    /// (fired from silence) picks up the new amount.
    #[test]
    fn vel_to_cutoff_captured_at_note_on() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_vel_routing(1.0, 1.0);
        press_vel(&mut synth, 60, 120);

        let mut buf = [0.0f32; 256];
        synth.render(&mut buf);
        let expected = 2_000.0 * ((120.0f32 / 127.0 - 0.5) * 2.0).exp2(); // ≈ 3705.6 Hz
        let captured = synth.voices[0].applied_cutoff_hz();
        assert!(
            (captured - expected).abs() < 1.0,
            "vel 120 with vel_to_cutoff=1 should retune to ≈{expected}, got {captured}"
        );

        // Knob off mid-note: the sounding note keeps its captured value.
        synth.set_vel_routing(0.0, 1.0);
        for _ in 0..10 {
            synth.render(&mut buf);
        }
        let held = synth.voices[0].applied_cutoff_hz();
        assert_eq!(
            held.to_bits(),
            captured.to_bits(),
            "mid-note routing change must not move the sounding note's cutoff \
             ({held} vs {captured})"
        );

        // Release to silence, then a new note captures the new (0) amount.
        release(&mut synth, 60);
        render_ms(&mut synth, 600);
        press_vel(&mut synth, 60, 120);
        synth.render(&mut buf);
        let fresh = synth.voices[0].applied_cutoff_hz();
        assert_eq!(
            fresh.to_bits(),
            2_000.0f32.to_bits(),
            "next note should capture vel_to_cutoff=0 (unmodulated base), got {fresh}"
        );
    }

    /// kbd-track = 1 opens the filter for high notes: note 84 (two
    /// octaves above the pivot 60) renders brighter than the same note
    /// with tracking off, and the ladder retunes to exactly 4× the base
    /// cutoff (2^(24/12) = 4).
    #[test]
    fn kbd_track_one_brightens_note_84() {
        let mut flat = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut tracked = NativeSynth::new("minimoog", 48_000.0).unwrap();
        tracked.set_kbd_track(1.0);

        press(&mut flat, 84);
        press(&mut tracked, 84);
        let a = render_ms(&mut flat, 100);
        let b = render_ms(&mut tracked, 100);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        let flat_cutoff = flat.voices[0].applied_cutoff_hz();
        let tracked_cutoff = tracked.voices[0].applied_cutoff_hz();
        assert_eq!(
            flat_cutoff.to_bits(),
            2_000.0f32.to_bits(),
            "untracked note 84 must stay at the base cutoff, got {flat_cutoff}"
        );
        assert!(
            (tracked_cutoff - 8_000.0).abs() < 0.5,
            "kbd_track=1 at note 84 should retune to 2000 * 2^2 = 8000 Hz, \
             got {tracked_cutoff}"
        );

        // Post-attack spectral check: the tracked render is brighter.
        let (ra, rb) = (hf_ratio(&mono(&a[960..])), hf_ratio(&mono(&b[960..])));
        assert!(
            rb > ra * 1.2,
            "kbd_track=1 should brighten note 84: hf_ratio tracked={rb} vs flat={ra}"
        );
    }

    /// Note 60 is the tracking pivot: with kbd_track = 1 the multiplier
    /// is exactly 2^0 = 1, so the render is bit-identical to an
    /// untracked synth.
    #[test]
    fn kbd_track_note_60_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut tracked = NativeSynth::new("minimoog", 48_000.0).unwrap();
        tracked.set_kbd_track(1.0);

        let a = render_note(&mut base);
        let b = render_note(&mut tracked);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged at the kbd-track pivot note: {x} vs {y}"
            );
        }
    }

    /// The three cutoff modulations compose multiplicatively (the
    /// canonical formula in `Voice::tick`): with the filter env at
    /// sustain, velocity routing at 1, and full keyboard tracking, the
    /// applied cutoff is
    /// base * 2^(amount*sustain*4) * 2^((vel/127-0.5)*2) * 2^((note-60)/12).
    #[test]
    fn cutoff_mods_compose_multiplicatively() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_cutoff_hz(500.0);
        synth.set_filter_env(0.005, 0.600, 0.4, 0.600, 1.0);
        synth.set_vel_routing(1.0, 1.0);
        synth.set_kbd_track(1.0);
        press_vel(&mut synth, 84, 120);

        // ~700 ms: decay (600 ms) done, env 2 sits at sustain 0.4.
        let mut buf = [0.0f32; 256];
        for _ in 0..263 {
            synth.render(&mut buf);
        }
        let applied = synth.voices[0].applied_cutoff_hz();
        let expected = 500.0
            * (1.0f32 * 0.4 * 4.0).exp2()
            * ((120.0f32 / 127.0 - 0.5) * 2.0).exp2()
            * (1.0f32 * (84.0 - 60.0) / 12.0).exp2(); // ≈ 11233 Hz
        assert!(
            (applied - expected).abs() < 25.0,
            "composed cutoff should be ≈{expected}, got {applied}"
        );
    }

    /// `set_vel_routing` clamps both amounts to [0, 1].
    #[test]
    fn set_vel_routing_clamps() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_vel_routing(2.0, -1.0);
        assert_eq!(synth.vel_to_cutoff, 1.0);
        assert_eq!(synth.vel_to_amp, 0.0);
        synth.set_vel_routing(-1.0, 2.0);
        assert_eq!(synth.vel_to_cutoff, 0.0);
        assert_eq!(synth.vel_to_amp, 1.0);
        synth.set_vel_routing(0.25, 0.5);
        assert_eq!(synth.vel_to_cutoff, 0.25);
        assert_eq!(synth.vel_to_amp, 0.5);
    }

    /// `set_kbd_track` clamps to [0, 1].
    #[test]
    fn set_kbd_track_clamps() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_kbd_track(2.0);
        assert_eq!(synth.kbd_track, 1.0);
        synth.set_kbd_track(-1.0);
        assert_eq!(synth.kbd_track, 0.0);
        synth.set_kbd_track(0.5);
        assert_eq!(synth.kbd_track, 0.5);
    }

    /// REGRESSION GUARANTEE (LFO stage): FNV-1a-64 over EVERY sample
    /// bit of the default 100 ms render. The golden hash was computed
    /// from the tree BEFORE the LFO / mod wheel / pitch bend stage
    /// landed — a whole-buffer strengthening of the 5-point golden in
    /// `default_render_matches_pre_change_golden`.
    #[test]
    fn default_render_full_buffer_checksum() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let samples = render_note(&mut synth);
        let mut h: u64 = 0xcbf2_9ce4_8422_2325;
        for s in &samples {
            for b in s.to_bits().to_le_bytes() {
                h ^= u64::from(b);
                h = h.wrapping_mul(0x0000_0100_0000_01b3);
            }
        }
        assert_eq!(
            h, 0x375f_57a6_c669_366d,
            "default render diverged from the pre-LFO-stage tree: FNV-1a-64 = {h:#018x}"
        );
    }

    /// REGRESSION GUARANTEE (LFO stage): explicitly pushing the LFO
    /// defaults (triangle, 5 Hz, all depths 0) and the bend-range
    /// default (2 semitones) is bit-transparent — the render matches an
    /// untouched synth sample for sample, even though the LFO itself
    /// runs. (The untouched-default path is separately pinned by
    /// `default_render_full_buffer_checksum`.)
    #[test]
    fn lfo_defaults_push_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut pushed = NativeSynth::new("minimoog", 48_000.0).unwrap();
        pushed.set_lfo(0, 5.0, 0.0, 0.0, 0.0);
        pushed.set_bend_range(2.0);

        let a = render_note(&mut base);
        let b = render_note(&mut pushed);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged after pushing default LFO params: {x} vs {y}"
            );
        }
    }

    /// Rising-crossing counts over the vibrato up-lobe vs down-lobe of
    /// a 1 Hz triangle LFO (cycle = 1 s, positive 0..500 ms, negative
    /// 500..1000 ms; windows trimmed 50 ms off each lobe edge). The
    /// difference measures pitch modulation depth.
    fn vibrato_updown_counts(synth: &mut NativeSynth) -> (usize, usize) {
        let samples = render_note_ms(synth, 1000);
        assert!(rms(&samples) > 0.05, "render audible");
        let up = rising_crossings_between(&samples, 50, 450);
        let down = rising_crossings_between(&samples, 550, 950);
        (up, down)
    }

    /// Vibrato at the boot wheel (1.0): a 100-cent depth on a 1 Hz
    /// triangle measurably modulates the pitch of a held note — the
    /// zero-crossing rate over the LFO's positive lobe exceeds the
    /// negative lobe's, while an unmodulated note reads (near-)equal
    /// rates over the same windows. This also pins the documented boot
    /// behavior: config depth is audible with NO CC 1 event ever sent.
    #[test]
    fn vibrato_modulates_pitch_at_boot_wheel() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let (bu, bd) = vibrato_updown_counts(&mut base);
        let base_spread = (bu as i32 - bd as i32).abs();
        assert!(
            base_spread <= 1,
            "unmodulated note should have a stable crossing rate: up={bu} down={bd}"
        );

        let mut vib = NativeSynth::new("minimoog", 48_000.0).unwrap();
        vib.set_lfo(0, 1.0, 100.0, 0.0, 0.0);
        let (vu, vd) = vibrato_updown_counts(&mut vib);
        // ±100 cents at the triangle's ±0.6 average over the windows
        // ≈ ±3.5% frequency ≈ ±3.6 crossings per 400 ms window at C4.
        assert!(
            vu as i32 - vd as i32 >= 4,
            "100-cent vibrato should raise the up-lobe crossing rate: up={vu} down={vd}"
        );
    }

    /// The mod wheel scales vibrato live: CC 1 = 0 silences a
    /// configured depth to bit-transparency, CC 1 = 127 reproduces the
    /// boot wheel (1.0) bit for bit, and CC 1 = 64 sits strictly
    /// between (weaker than full, stronger than none).
    #[test]
    fn mod_wheel_scales_vibrato() {
        let cc = |synth: &mut NativeSynth, value: u8| {
            synth.handle_event(&MidiEvent::ControlChange {
                channel: 0,
                controller: 1,
                value,
            });
        };

        // Wheel 0: bit-identical to an untouched synth despite the
        // configured 100-cent depth.
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut wheel0 = NativeSynth::new("minimoog", 48_000.0).unwrap();
        wheel0.set_lfo(0, 1.0, 100.0, 0.0, 0.0);
        cc(&mut wheel0, 0);
        let a = render_note(&mut base);
        let b = render_note(&mut wheel0);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i}: wheel 0 must silence vibrato bit-exactly: {x} vs {y}"
            );
        }

        // Wheel 127 (= 1.0): bit-identical to the boot wheel.
        let mut boot = NativeSynth::new("minimoog", 48_000.0).unwrap();
        boot.set_lfo(0, 1.0, 100.0, 0.0, 0.0);
        let mut wheel127 = NativeSynth::new("minimoog", 48_000.0).unwrap();
        wheel127.set_lfo(0, 1.0, 100.0, 0.0, 0.0);
        cc(&mut wheel127, 127);
        let c = render_note(&mut boot);
        let d = render_note(&mut wheel127);
        for (i, (x, y)) in c.iter().zip(&d).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i}: CC 1 = 127 must equal the boot wheel: {x} vs {y}"
            );
        }

        // Wheel 64 (≈ 0.504): pitch spread strictly between 0 and full.
        let mut full = NativeSynth::new("minimoog", 48_000.0).unwrap();
        full.set_lfo(0, 1.0, 100.0, 0.0, 0.0);
        let (fu, fd) = vibrato_updown_counts(&mut full);
        let full_spread = fu as i32 - fd as i32;

        let mut half = NativeSynth::new("minimoog", 48_000.0).unwrap();
        half.set_lfo(0, 1.0, 100.0, 0.0, 0.0);
        cc(&mut half, 64);
        let (hu, hd) = vibrato_updown_counts(&mut half);
        let half_spread = hu as i32 - hd as i32;

        assert!(
            half_spread >= 2 && half_spread < full_spread,
            "wheel 64 should scale vibrato to roughly half: \
             half spread={half_spread}, full spread={full_spread}"
        );
    }

    /// Tremolo (LFO → amp = 1, 5 Hz triangle) modulates the RMS
    /// envelope at the LFO rate: near-silent windows around the LFO
    /// peaks (250 ms, 450 ms — amp factor → 0) and loud windows around
    /// the troughs (350 ms — amp factor → 1), period 200 ms.
    #[test]
    fn tremolo_modulates_rms_at_lfo_rate() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_lfo(0, 5.0, 0.0, 0.0, 1.0);
        let samples = render_note_ms(&mut synth, 600);

        // 5 Hz triangle from phase 0: peaks (+1) at 50/250/450 ms,
        // troughs (-1) at 150/350/550 ms. Skip the first cycle (attack
        // + decay transient), probe ±20 ms windows in steady state.
        let quiet1 = rms(&samples[230 * 96..270 * 96]);
        let loud = rms(&samples[330 * 96..370 * 96]);
        let quiet2 = rms(&samples[430 * 96..470 * 96]);
        assert!(loud > 0.05, "tremolo trough should be loud, rms={loud}");
        assert!(
            loud > quiet1 * 3.0 && loud > quiet2 * 3.0,
            "tremolo should dip the level at the LFO peaks: \
             loud={loud} quiet@250ms={quiet1} quiet@450ms={quiet2}"
        );
    }

    /// LFO → cutoff (2 octaves on a 2.5 Hz triangle over a 500 Hz base
    /// cutoff) changes brightness periodically: the hf-ratio near an
    /// LFO peak (cutoff × 4) beats the hf-ratio near a trough
    /// (cutoff ÷ 4), cycle after cycle.
    #[test]
    fn cutoff_lfo_modulates_brightness_periodically() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_cutoff_hz(500.0);
        synth.set_lfo(0, 2.5, 0.0, 2.0, 0.0);
        let samples = render_note_ms(&mut synth, 1200);

        // 2.5 Hz triangle from phase 0: peaks at 100/500/900 ms,
        // troughs at 300/700/1100 ms. Skip the first cycle; probe
        // ±40 ms windows around the second and third cycles' extremes.
        let m = mono(&samples);
        let bright1 = hf_ratio(&m[460 * 48..540 * 48]);
        let dark1 = hf_ratio(&m[660 * 48..740 * 48]);
        let bright2 = hf_ratio(&m[860 * 48..940 * 48]);
        let dark2 = hf_ratio(&m[1060 * 48..1140 * 48]);
        assert!(
            bright1 > dark1 * 1.3 && bright2 > dark2 * 1.3,
            "cutoff LFO should brighten at peaks and darken at troughs, \
             cycle 2: {bright1} vs {dark1}, cycle 3: {bright2} vs {dark2}"
        );
    }

    /// Pitch bend at full deflection with the default ±2-semitone
    /// range shifts the fundamental by 2^(2·(8191/8192)/12) ≈ 1.122 —
    /// measured via rising-crossing counts over a 900 ms window.
    #[test]
    fn bend_up_two_semitones_shifts_fundamental() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut bent = NativeSynth::new("minimoog", 48_000.0).unwrap();
        bent.handle_event(&MidiEvent::PitchBend {
            channel: 0,
            bend: 16383,
        });

        let a = render_note_ms(&mut base, 1000);
        let b = render_note_ms(&mut bent, 1000);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        let ca = rising_crossings_between(&a, 100, 1000);
        let cb = rising_crossings_between(&b, 100, 1000);
        let ratio = cb as f32 / ca as f32;
        assert!(
            (1.10..=1.15).contains(&ratio),
            "+2 st bend should scale the fundamental by ≈1.122: \
             base={ca}, bent={cb}, ratio={ratio}"
        );
    }

    /// A centered bend (wire value 8192 ⇒ norm 0 ⇒ factor exp2(0) = 1)
    /// is bit-transparent.
    #[test]
    fn bend_center_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut centered = NativeSynth::new("minimoog", 48_000.0).unwrap();
        centered.handle_event(&MidiEvent::PitchBend {
            channel: 0,
            bend: 8192,
        });

        let a = render_note(&mut base);
        let b = render_note(&mut centered);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged on a centered bend: {x} vs {y}"
            );
        }
    }

    /// The bend range scales the deflection: at range 12 a full
    /// downward bend (wire 0 ⇒ norm -1) drops the fundamental a whole
    /// octave (crossing ratio ≈ 0.5).
    #[test]
    fn bend_range_scales_deflection() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut dive = NativeSynth::new("minimoog", 48_000.0).unwrap();
        dive.set_bend_range(12.0);
        dive.handle_event(&MidiEvent::PitchBend {
            channel: 0,
            bend: 0,
        });

        let a = render_note_ms(&mut base, 1000);
        let b = render_note_ms(&mut dive, 1000);
        assert!(rms(&a) > 0.05 && rms(&b) > 0.05, "both renders audible");

        let ca = rising_crossings_between(&a, 100, 1000);
        let cb = rising_crossings_between(&b, 100, 1000);
        let ratio = cb as f32 / ca as f32;
        assert!(
            (0.45..=0.55).contains(&ratio),
            "range 12 + full down-bend should halve the fundamental: \
             base={ca}, bent={cb}, ratio={ratio}"
        );
    }

    /// The S&H waveform is deterministic: two identically-driven synths
    /// render bit-identically (fixed xorshift seed), and the modulation
    /// is audible (differs from an unmodulated synth).
    #[test]
    fn sample_hold_lfo_is_deterministic() {
        let mut a_synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut b_synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        a_synth.set_lfo(3, 8.0, 100.0, 1.0, 0.0);
        b_synth.set_lfo(3, 8.0, 100.0, 1.0, 0.0);

        let a = render_note_ms(&mut a_synth, 500);
        let b = render_note_ms(&mut b_synth, 500);
        assert!(rms(&a) > 0.05, "S&H render audible");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i}: identical S&H synths diverged: {x} vs {y}"
            );
        }

        let mut plain = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let c = render_note_ms(&mut plain, 500);
        let max_diff = a
            .iter()
            .zip(&c)
            .map(|(x, y)| (x - y).abs())
            .fold(0.0f32, f32::max);
        assert!(
            max_diff > 0.01,
            "S&H modulation should audibly change the render, max diff={max_diff}"
        );
    }

    /// `set_lfo` clamps every argument and falls back to triangle on an
    /// invalid wave code (the FFI boundary rejects those upstream).
    #[test]
    fn set_lfo_clamps_and_falls_back_on_bad_wave() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_lfo(9, 100.0, 500.0, 7.0, 3.0);
        assert_eq!(synth.lfo.wave(), LfoWave::Triangle);
        assert_eq!(synth.lfo.rate_hz(), 20.0);
        assert_eq!(synth.lfo_to_pitch_cents, 100.0);
        assert_eq!(synth.lfo_to_cutoff_oct, 2.0);
        assert_eq!(synth.lfo_to_amp, 1.0);

        synth.set_lfo(2, 0.0, -5.0, -1.0, -1.0);
        assert_eq!(synth.lfo.wave(), LfoWave::Square);
        assert_eq!(synth.lfo.rate_hz(), 0.05);
        assert_eq!(synth.lfo_to_pitch_cents, 0.0);
        assert_eq!(synth.lfo_to_cutoff_oct, 0.0);
        assert_eq!(synth.lfo_to_amp, 0.0);

        synth.set_lfo(1, 4.0, 30.0, 0.5, 0.25);
        assert_eq!(synth.lfo.wave(), LfoWave::Saw);
        assert_eq!(synth.lfo.rate_hz(), 4.0);
        assert_eq!(synth.lfo_to_pitch_cents, 30.0);
        assert_eq!(synth.lfo_to_cutoff_oct, 0.5);
        assert_eq!(synth.lfo_to_amp, 0.25);
    }

    /// `set_bend_range` clamps to [0, 12] semitones and retunes the
    /// cached bend factor for an already-deflected wheel.
    #[test]
    fn set_bend_range_clamps() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_bend_range(100.0);
        assert_eq!(synth.bend_range_semitones, 12.0);
        synth.set_bend_range(-1.0);
        assert_eq!(synth.bend_range_semitones, 0.0);
        synth.set_bend_range(2.0);
        assert_eq!(synth.bend_range_semitones, 2.0);

        // Range change while deflected recomputes the factor: full up
        // at range 0 is exactly 1.0; widening to 12 retunes it.
        synth.set_bend_range(0.0);
        synth.handle_event(&MidiEvent::PitchBend {
            channel: 0,
            bend: 16383,
        });
        assert_eq!(synth.bend_factor.to_bits(), 1.0f32.to_bits());
        synth.set_bend_range(12.0);
        let expected = (12.0f32 * (8191.0 / 8192.0) / 12.0).exp2();
        assert_eq!(synth.bend_factor.to_bits(), expected.to_bits());
    }

    /// REGRESSION GUARANTEE (stage 4 poly): explicitly pushing the
    /// default voice mode (0 = mono_legato) is bit-transparent — the
    /// render matches an untouched synth sample for sample. (The
    /// untouched-default path is separately pinned to the pre-change
    /// golden bits by `default_render_matches_pre_change_golden` and
    /// `default_render_full_buffer_checksum`, which now exercise the
    /// 8-voice pool and the mode-dispatched allocator at their
    /// defaults.)
    #[test]
    fn voice_mode_default_push_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut pushed = NativeSynth::new("minimoog", 48_000.0).unwrap();
        pushed.set_voice_mode(0);

        let a = render_note(&mut base);
        let b = render_note(&mut pushed);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged after pushing default voice mode: {x} vs {y}"
            );
        }
    }

    /// `set_voice_mode` maps the 0/1/2 wire codes and falls back to
    /// mono_legato on an invalid code (the FFI boundary rejects those
    /// upstream, mirroring the osc/LFO wave handling).
    #[test]
    fn set_voice_mode_invalid_code_falls_back_to_mono_legato() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        assert_eq!(synth.voice_mode, VoiceMode::MonoLegato);
        synth.set_voice_mode(2);
        assert_eq!(synth.voice_mode, VoiceMode::Poly);
        synth.set_voice_mode(9);
        assert_eq!(synth.voice_mode, VoiceMode::MonoLegato);
        synth.set_voice_mode(1);
        assert_eq!(synth.voice_mode, VoiceMode::MonoRetrig);
    }

    /// Poly mode sounds a 3-note chord on three voices at once. Each
    /// note is also rendered solo (fresh synth, same params) to pin
    /// three DISTINCT fundamentals via the zero-crossing heuristic
    /// (C3 ≈ 130.8 Hz, C4 ≈ 261.6 Hz, G4 ≈ 392 Hz over a 400 ms
    /// post-attack window); the chord then must carry more energy than
    /// any solo render (three simultaneous sources) with no voice cut
    /// (all three voices still sounding their notes after the render).
    /// Releasing one chord note releases exactly that voice.
    #[test]
    fn poly_chord_renders_three_distinct_fundamentals() {
        let notes: [u8; 3] = [48, 60, 67];
        // Expected rising crossings over the 100..500 ms window:
        // freq * 0.4 s, ±10%.
        let expected: [(usize, usize); 3] = [(47, 58), (94, 115), (141, 173)];

        let mut solo_rms = [0.0f32; 3];
        for (i, &n) in notes.iter().enumerate() {
            let mut solo = NativeSynth::new("minimoog", 48_000.0).unwrap();
            solo.set_voice_mode(2);
            press(&mut solo, n);
            let buf = render_ms(&mut solo, 500);
            assert!(rms(&buf) > 0.05, "solo note {n} audible");
            solo_rms[i] = rms(&buf);
            let c = rising_crossings_between(&buf, 100, 500);
            let (lo, hi) = expected[i];
            assert!(
                (lo..=hi).contains(&c),
                "solo note {n} fundamental off: {c} crossings/400 ms, want {lo}..={hi}"
            );
        }

        let mut chord = NativeSynth::new("minimoog", 48_000.0).unwrap();
        chord.set_voice_mode(2);
        for &n in &notes {
            press(&mut chord, n);
        }
        let buf = render_ms(&mut chord, 500);
        let chord_rms = rms(&buf);
        for (i, &n) in notes.iter().enumerate() {
            assert!(
                chord_rms > solo_rms[i],
                "chord should out-power the solo note {n}: chord rms={chord_rms}, \
                 solo rms={}",
                solo_rms[i]
            );
        }
        // No voice cut: all three notes still sounding on live voices.
        let sounding = chord.voice_notes();
        for &n in &notes {
            assert!(
                sounding.contains(&Some(n)),
                "note {n} was cut from the chord: sounding voices {sounding:?}"
            );
        }
        assert_eq!(
            chord.voices.iter().filter(|v| v.is_active()).count(),
            3,
            "exactly three voices should sound"
        );

        // note_off releases exactly the matching voice: after releasing
        // C4 and rendering past the 400 ms amp release, two voices
        // remain and C4 is gone.
        release(&mut chord, 60);
        render_ms(&mut chord, 600);
        let remaining = chord.voice_notes();
        assert!(
            remaining.contains(&Some(48)) && remaining.contains(&Some(67)),
            "held chord notes must keep sounding: {remaining:?}"
        );
        assert!(
            !remaining.contains(&Some(60)),
            "released note 60 should be gone: {remaining:?}"
        );
        assert_eq!(
            chord.voices.iter().filter(|v| v.is_active()).count(),
            2,
            "exactly the two held voices should sound after the release"
        );
    }

    /// Steal policy (§1.2 "oldest" v1): with all 8 voices sounding, the
    /// 9th note steals the FIRST-fired voice (lowest `fired_at`) and
    /// leaves the other seven untouched.
    #[test]
    fn poly_ninth_note_steals_oldest_fired_voice() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_voice_mode(2);
        let notes: [u8; 8] = [48, 50, 52, 53, 55, 57, 59, 60];
        for &n in &notes {
            press(&mut synth, n);
        }
        render_ms(&mut synth, 50);
        let before = synth.voice_notes();
        for &n in &notes {
            assert!(
                before.contains(&Some(n)),
                "note {n} should be sounding before the steal: {before:?}"
            );
        }
        assert_eq!(
            synth.voices.iter().filter(|v| v.is_active()).count(),
            8,
            "all 8 voices should sound before the steal"
        );

        press(&mut synth, 62); // 9th note — pool is full.
        let after = synth.voice_notes();
        let stolen = before
            .iter()
            .position(|&n| n == Some(48))
            .expect("first-fired note present");
        assert_eq!(
            after[stolen],
            Some(62),
            "9th note should steal the first-fired voice (had note 48): {after:?}"
        );
        for (i, (&b, &a)) in before.iter().zip(after.iter()).enumerate() {
            if i != stolen {
                assert_eq!(a, b, "voice {i} must be untouched by the steal");
            }
        }
    }

    /// mono_retrig re-fires the amp envelope on every note-on where
    /// mono_legato (a key already held) does not. With this `Adsr` the
    /// retrigger re-enters the attack stage FROM THE CURRENT VALUE (no
    /// reset-to-zero click — see `Adsr::note_on`), so the audible
    /// signature of a retrigger onto a sustaining voice is a swell:
    /// sustain 0.7 → attack to 1.0 → 200 ms decay back to 0.7. Probe:
    /// re-press the held note (same pitch — no filter/pitch transient
    /// confound) and compare an early post-press window against the
    /// re-settled tail; legato stays flat, retrig is measurably hotter
    /// early.
    #[test]
    fn mono_retrig_retriggers_envelope_where_legato_does_not() {
        let contour = |mode: u32| -> (f32, f32) {
            let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
            synth.set_voice_mode(mode);
            press(&mut synth, 60);
            render_ms(&mut synth, 400); // settle at sustain 0.7
            press(&mut synth, 60); // re-press, key still held
            let buf = render_ms(&mut synth, 300);
            (rms(&buf[5 * 96..100 * 96]), rms(&buf[250 * 96..300 * 96]))
        };

        let (leg_early, leg_late) = contour(0);
        let (ret_early, ret_late) = contour(1);
        assert!(leg_late > 0.05 && ret_late > 0.05, "both renders audible");

        assert!(
            (leg_early / leg_late - 1.0).abs() < 0.05,
            "mono_legato must NOT retrigger on a held-key press (flat sustain): \
             early rms={leg_early}, late rms={leg_late}"
        );
        assert!(
            ret_early > ret_late * 1.15,
            "mono_retrig should swell through the retriggered attack+decay: \
             early rms={ret_early}, late rms={ret_late}"
        );
        assert!(
            (ret_late / leg_late - 1.0).abs() < 0.05,
            "both modes settle back to the same sustain: retrig={ret_late}, \
             legato={leg_late}"
        );
    }

    /// Switching modes mid-chord releases every voice cleanly (the
    /// documented `set_voice_mode` policy): the chord decays through
    /// the normal 400 ms amp release to exact silence, every voice
    /// goes idle, the held-notes stack is cleared, the old keys' late
    /// note-offs are harmless no-ops, and a new press under the new
    /// mode sounds normally.
    #[test]
    fn mode_switch_mid_chord_releases_cleanly() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_voice_mode(2);
        for &n in &[48u8, 60, 67] {
            press(&mut synth, n);
        }
        let sounding = render_ms(&mut synth, 100);
        assert!(rms(&sounding) > 0.05, "chord audible before the switch");

        synth.set_voice_mode(0); // poly -> mono_legato mid-chord
        assert!(
            synth.held_notes.is_empty(),
            "mode switch must clear the held-notes stack"
        );
        let tail = render_ms(&mut synth, 600);
        assert!(
            rms(&tail[..100 * 96]) > 1e-3,
            "release tails should still be fading right after the switch"
        );
        assert!(
            tail[500 * 96..].iter().all(|&s| s == 0.0),
            "chord must decay to exact silence after the 400 ms release"
        );
        assert!(
            synth.voices.iter().all(|v| !v.is_active()),
            "every voice must be idle after the mode-switch release"
        );

        // The physically-still-held keys' note-offs are no-ops...
        for &n in &[48u8, 60, 67] {
            release(&mut synth, n);
        }
        let quiet = render_ms(&mut synth, 100);
        assert!(
            quiet.iter().all(|&s| s == 0.0),
            "stale note-offs after a mode switch must stay silent"
        );

        // ...and a fresh press under the new mode plays normally.
        press(&mut synth, 52);
        let fresh = render_ms(&mut synth, 100);
        assert!(rms(&fresh) > 0.05, "new note after the switch audible");
    }

    /// CPU sanity (§1.5 budget: 8 voices ≈ 3–8% of one core): 8
    /// sounding poly voices with a worst-case-ish patch — all three
    /// oscillators + noise audible, drive, glide, kbd-tracking, filter
    /// env, and all three LFO depths live — must render 1 s of 48 kHz
    /// audio well under 1 s of wall time in release mode. Measured on
    /// the dev machine (release, 2026-07): ~43 ms per rendered second —
    /// roughly 23x realtime, i.e. ~4% of one core, inside the §1.5
    /// budget (3–8%). The assert allows 500 ms (2x realtime) so slow CI
    /// boxes don't flake; debug builds skip the timing assert (they're
    /// ~10-20x slower and not what ships).
    #[test]
    fn poly_eight_voices_render_faster_than_realtime() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_voice_mode(2);
        synth.set_osc(0, 0, 0, 0.0, 1.0);
        synth.set_osc(1, 1, 0, -7.0, 1.0);
        synth.set_osc(2, 2, -1, 5.0, 1.0);
        synth.set_noise_level(1.0);
        synth.set_drive(0.5);
        synth.set_glide(0.05);
        synth.set_kbd_track(1.0);
        synth.set_filter_env(0.005, 0.6, 0.4, 0.6, 1.0);
        synth.set_lfo(0, 5.0, 50.0, 1.0, 0.5);
        for &n in &[48u8, 50, 52, 53, 55, 57, 59, 60] {
            press(&mut synth, n);
        }

        let mut buf = [0.0f32; 256]; // 128 frames per block
        let start = std::time::Instant::now();
        for _ in 0..375 {
            // 375 blocks * 128 frames = 48 000 frames = 1 s @ 48 kHz
            synth.render(&mut buf);
        }
        let elapsed = start.elapsed();
        assert_eq!(
            synth.voices.iter().filter(|v| v.is_active()).count(),
            8,
            "all 8 voices should still be sounding (sustain, no note-offs)"
        );
        if !cfg!(debug_assertions) {
            assert!(
                elapsed < std::time::Duration::from_millis(500),
                "8-voice poly render of 1 s of audio took {elapsed:?} — \
                 expected well under realtime in release mode"
            );
        }
    }

    /// Sum of squared DFT magnitudes over the `[lo_hz, hi_hz]` band of
    /// a Hann-windowed mono slice, evaluated per bin with Goertzel at
    /// the slice's natural resolution (`sample_rate / len`). Used as an
    /// aliasing-energy probe: fold-back components are discrete lines,
    /// so summing every bin in the band catches them wherever they
    /// land. f64 accumulation keeps long windows honest.
    fn band_energy(m: &[f32], sample_rate: f32, lo_hz: f32, hi_hz: f32) -> f64 {
        let n = m.len();
        let windowed: Vec<f64> = m
            .iter()
            .enumerate()
            .map(|(i, &x)| {
                let w = 0.5 - 0.5 * (2.0 * std::f64::consts::PI * i as f64 / n as f64).cos();
                f64::from(x) * w
            })
            .collect();
        let bin_hz = f64::from(sample_rate) / n as f64;
        let lo_bin = (f64::from(lo_hz) / bin_hz).ceil() as usize;
        let hi_bin = ((f64::from(hi_hz) / bin_hz).floor() as usize).min(n / 2);
        let mut total = 0.0f64;
        for k in lo_bin..=hi_bin {
            let w = 2.0 * std::f64::consts::PI * k as f64 / n as f64;
            let coeff = 2.0 * w.cos();
            let (mut s1, mut s2) = (0.0f64, 0.0f64);
            for &x in &windowed {
                let s0 = x + coeff * s1 - s2;
                s2 = s1;
                s1 = s0;
            }
            total += s1 * s1 + s2 * s2 - coeff * s1 * s2;
        }
        total
    }

    /// REGRESSION GUARANTEE (stage 5): explicitly pushing the default
    /// oversample=false through the setter is bit-transparent — the
    /// render matches an untouched synth sample for sample. (The
    /// untouched-default path is separately pinned to the pre-change
    /// golden bits by `default_render_matches_pre_change_golden`.)
    #[test]
    fn oversample_off_push_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut pushed = NativeSynth::new("minimoog", 48_000.0).unwrap();
        pushed.set_oversample(false);

        let a = render_note(&mut base);
        let b = render_note(&mut pushed);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged after pushing oversample=false: {x} vs {y}"
            );
        }
    }

    /// Toggling oversampling on and back off while every voice is idle
    /// must leave no trace: the swap resets the newly-active ladder
    /// (state was already zero) and retunes it to the same cutoff/Q it
    /// was built with, so a later note renders bit-identically to an
    /// untouched synth. This pins the "setup switch flipped in the UI
    /// before playing" path.
    #[test]
    fn oversample_idle_toggle_roundtrip_is_bit_transparent() {
        let mut base = NativeSynth::new("minimoog", 48_000.0).unwrap();
        let mut toggled = NativeSynth::new("minimoog", 48_000.0).unwrap();
        // Render between the toggles so the per-block push actually
        // reaches the (idle) voices and exercises both voice-level
        // swaps.
        toggled.set_oversample(true);
        render_ms(&mut toggled, 50);
        toggled.set_oversample(false);
        render_ms(&mut toggled, 50);
        // Keep the free-running global LFO phase in step.
        render_ms(&mut base, 100);

        let a = render_note(&mut base);
        let b = render_note(&mut toggled);
        assert!(rms(&a) > 0.05, "expected audible render");
        for (i, (x, y)) in a.iter().zip(&b).enumerate() {
            assert_eq!(
                x.to_bits(),
                y.to_bits(),
                "sample {i} diverged after an idle oversample round-trip: {x} vs {y}"
            );
        }
    }

    /// The point of stage 5 (ROADMAP §0.1 / §1.6): with hard drive and
    /// a wide-open filter on a high note, the tanh stages spray
    /// harmonics past Nyquist that fold back into the audible band at
    /// 48 kHz. The 2× oversampled path must carry less energy above
    /// 15 kHz relative to the fundamental band — and be audibly
    /// non-broken doing it (finite, non-silent, RMS within 3 dB of the
    /// base path).
    #[test]
    fn oversample_reduces_aliasing_with_high_drive() {
        let build = |on: bool| {
            let mut s = NativeSynth::new("minimoog", 48_000.0).unwrap();
            s.set_cutoff_hz(20_000.0); // wide open — the ladder hides nothing
            s.set_drive(1.0); // pre-gain 5: saw -> near-square, dense harmonics
            s.set_oversample(on);
            s
        };
        let mut off = build(false);
        let mut on = build(true);
        // Note 100 (E7 ≈ 2637 Hz): harmonics land every ~2.6 kHz, so
        // plenty fall past 24 kHz and fold back at the base rate.
        press_vel(&mut off, 100, 127);
        press_vel(&mut on, 100, 127);
        let a = render_ms(&mut off, 600);
        let b = render_ms(&mut on, 600);

        // Audibly non-broken: finite, non-silent, level within 3 dB.
        assert!(
            b.iter().all(|s| s.is_finite()),
            "oversampled render must be finite"
        );
        let (ra, rb) = (rms(&a), rms(&b));
        assert!(
            ra > 0.02 && rb > 0.02,
            "both renders audible: rms off={ra} on={rb}"
        );
        let level_db = 20.0 * (rb / ra).log10();
        assert!(
            level_db.abs() <= 3.0,
            "oversampled RMS must stay within 3 dB of the base path, got {level_db:.2} dB \
             (off={ra}, on={rb})"
        );

        // Aliasing probe over a steady sustain slice (skip the 100 ms
        // attack/decay transient): energy above 15 kHz relative to the
        // fundamental band (100 Hz – 5 kHz, fundamental + first
        // harmonic). The oversampled ratio must not exceed the base
        // ratio (5% measurement tolerance).
        let wa = &mono(&a[100 * 96..])[..8192];
        let wb = &mono(&b[100 * 96..])[..8192];
        let base_off = band_energy(wa, 48_000.0, 100.0, 5_000.0);
        let base_on = band_energy(wb, 48_000.0, 100.0, 5_000.0);
        assert!(
            base_off > 0.0 && base_on > 0.0,
            "fundamental-band energy present in both renders"
        );
        let hf_off = band_energy(wa, 48_000.0, 15_000.0, 23_900.0) / base_off;
        let hf_on = band_energy(wb, 48_000.0, 15_000.0, 23_900.0) / base_on;
        assert!(
            hf_on <= hf_off * 1.05,
            "oversampling must not add HF junk: relative >15 kHz energy on={hf_on:.3e} \
             vs off={hf_off:.3e}"
        );
    }

    /// Toggling oversampling MID-NOTE is allowed (with a documented
    /// click risk — see `Voice::set_oversample`): the voice must stay
    /// finite and audible through both swap directions, and keep
    /// sounding afterwards.
    #[test]
    fn oversample_toggle_mid_note_stays_finite() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_drive(1.0);
        press(&mut synth, 60);
        let before = render_ms(&mut synth, 100);
        assert!(rms(&before) > 0.02, "note audible before the toggle");

        synth.set_oversample(true);
        let during = render_ms(&mut synth, 200);
        assert!(
            during.iter().all(|s| s.is_finite()),
            "render must stay finite after toggling oversampling ON mid-note"
        );
        assert!(rms(&during) > 0.02, "note still audible while oversampled");

        synth.set_oversample(false);
        let after = render_ms(&mut synth, 200);
        assert!(
            after.iter().all(|s| s.is_finite()),
            "render must stay finite after toggling oversampling OFF mid-note"
        );
        assert!(
            rms(&after) > 0.02,
            "note still audible after the round-trip"
        );
        assert!(synth.voices[0].is_active(), "voice still sounding");
    }

    /// CPU sanity for the oversampled path: the same worst-case-ish
    /// 8-voice poly patch as `poly_eight_voices_render_faster_than_
    /// realtime`, but with the 2× drive+ladder engaged — 1 s of 48 kHz
    /// audio must render well under 1 s of wall time in release mode.
    /// Measured on the dev machine (release, 2026-07): ~84 ms per
    /// rendered second (vs ~43 ms base) — the halfband FIRs + doubled
    /// ladder rate roughly double the voice cost, still ~8% of one
    /// core, at the top of but inside the §1.5 budget. The assert allows 500 ms (2× realtime) so slow CI boxes
    /// don't flake; debug builds skip the timing assert.
    #[test]
    fn oversampled_eight_voices_render_faster_than_realtime() {
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_voice_mode(2);
        synth.set_oversample(true);
        synth.set_osc(0, 0, 0, 0.0, 1.0);
        synth.set_osc(1, 1, 0, -7.0, 1.0);
        synth.set_osc(2, 2, -1, 5.0, 1.0);
        synth.set_noise_level(1.0);
        synth.set_drive(1.0);
        synth.set_glide(0.05);
        synth.set_kbd_track(1.0);
        synth.set_filter_env(0.005, 0.6, 0.4, 0.6, 1.0);
        synth.set_lfo(0, 5.0, 50.0, 1.0, 0.5);
        for &n in &[48u8, 50, 52, 53, 55, 57, 59, 60] {
            press(&mut synth, n);
        }

        let mut buf = [0.0f32; 256]; // 128 frames per block
        let start = std::time::Instant::now();
        for _ in 0..375 {
            // 375 blocks * 128 frames = 48 000 frames = 1 s @ 48 kHz
            synth.render(&mut buf);
        }
        let elapsed = start.elapsed();
        assert_eq!(
            synth.voices.iter().filter(|v| v.is_active()).count(),
            8,
            "all 8 voices should still be sounding (sustain, no note-offs)"
        );
        assert!(
            buf.iter().all(|s| s.is_finite()),
            "oversampled poly render must stay finite"
        );
        if !cfg!(debug_assertions) {
            assert!(
                elapsed < std::time::Duration::from_millis(500),
                "8-voice oversampled render of 1 s of audio took {elapsed:?} — \
                 expected well under realtime in release mode"
            );
        }
    }
}
