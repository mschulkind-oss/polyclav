//! Native pure-Rust analog-style synth backend.
//!
//! Phase 1 of `docs/ROADMAP.md`: a single hardcoded
//! "minimoog" engine — one PolyBLEP saw oscillator into a Moog ladder
//! filter into an ADSR amp env — rendered into the same interleaved
//! stereo buffer the rest of `audio-core` uses. No multi-osc, no LFO,
//! no mod matrix, no glide; just enough infrastructure to prove the
//! backend works through PipeWire.
//!
//! ## Voice allocator
//!
//! The allocator scaffolding is sized to support both mono-legato
//! (Minimoog tradition) and poly modes from day one — even though
//! Phase 1 only ships mono-legato. The voice pool is fixed at 4 voices,
//! enough for the eventual poly default. Phase 1 simply picks voice 0
//! for every note in mono mode; poly stealing is left as a TODO that
//! Phase 3 will fill in (`docs/ROADMAP.md` Phase 3).

mod envelope;
mod filter;
mod oscillator;
mod voice;

use crate::MidiEvent;
use voice::Voice;

/// Maximum voices held in the allocator's pool. 4 is the Phase 1 cap
/// (Minimoog runs mono — only voice 0 ever fires — but the pool is
/// pre-sized for the eventual poly switch).
const MAX_VOICES: usize = 4;

/// Voice allocation strategy. Phase 1 always uses `MonoLegato`; the
/// other modes exist so the scaffolding doesn't paint us into a corner
/// (see doc 14 §7 / §8 "Phase 1 decisions (locked in)").
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(dead_code)] // MonoRetrig/Poly reserved for Phase 2+; switch via patch loader.
pub enum VoiceMode {
    /// Single voice, last-note priority, envelopes only retrigger when
    /// no note is currently held.
    MonoLegato,
    /// Single voice, last-note priority, envelopes ALWAYS retrigger.
    /// Stubbed for Phase 2.
    MonoRetrig,
    /// Multi-voice with stealing. Stubbed for Phase 3 — Phase 1 falls
    /// back to using voice 0 only (clearly TODO'd in `note_on`).
    Poly,
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
}

impl NativeSynth {
    /// Build a synth for the given engine name. `sample_rate` is in Hz.
    pub fn new(engine_name: &str, sample_rate: f32) -> Result<Self, String> {
        let engine = Engine::parse(engine_name)?;
        let voices = (0..MAX_VOICES).map(|_| Voice::new(sample_rate)).collect();
        let mut synth = Self {
            engine,
            sample_rate,
            // Phase 1: Minimoog ships in mono-legato. The mode lives in
            // the patch (doc 14 §6.1) — once the patch loader exists,
            // this will be a per-patch field.
            voice_mode: VoiceMode::MonoLegato,
            voices,
            held_notes: Vec::with_capacity(16),
            next_fire_id: 0,
            cutoff_hz: 2_000.0,
            resonance: 0.3,
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

    /// Push a MIDI event into the synth. Phase 1 only acts on NoteOn /
    /// NoteOff; CC and pitch bend are accepted silently for forward
    /// compatibility (Phase 2 will route mod wheel + bend).
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
            MidiEvent::ControlChange { .. } | MidiEvent::PitchBend { .. } => {
                // Phase 1: routed elsewhere (knob events go through the
                // DSP atomics, not the MIDI queue). Other CCs/bend are
                // silently dropped until Phase 2 wires the mod matrix.
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
        match self.voice_mode {
            VoiceMode::MonoLegato | VoiceMode::MonoRetrig => {
                // Mono modes always fire voice 0. MonoLegato suppresses
                // envelope retrigger only when another key is already
                // held — a new note arriving during release (no keys
                // held) DOES retrigger the envelope.
                let suppress_retrigger = self.voice_mode == VoiceMode::MonoLegato && other_key_held;
                self.voices[0].note = Some(note);
                self.voices[0].velocity_scale = (velocity as f32) / 127.0;
                self.voices[0].fired_at = fire_id;
                if !suppress_retrigger {
                    // Re-call note_on to retrigger envelopes from scratch.
                    self.voices[0].note_on(note, velocity, fire_id);
                }
            }
            VoiceMode::Poly => {
                // TODO(phase-3): full LRU steal policy per doc 14 §2.2.
                // For Phase 1 we just fire voice 0 — that keeps the
                // scaffolding shape valid without claiming poly works.
                self.voices[0].note_on(note, velocity, fire_id);
            }
        }
    }

    fn note_off(&mut self, note: u8) {
        self.held_notes.retain(|&n| n != note);
        match self.voice_mode {
            VoiceMode::MonoLegato | VoiceMode::MonoRetrig => {
                if self.voices[0].note == Some(note) {
                    if let Some(&prev) = self.held_notes.last() {
                        // Fall back to the most recently-held remaining
                        // note (last-note priority). Don't retrigger
                        // envelopes — this is a legato hand-off.
                        self.voices[0].note = Some(prev);
                    } else {
                        // No keys held — release the voice.
                        self.voices[0].note_off();
                    }
                }
            }
            VoiceMode::Poly => {
                // TODO(phase-3): release exactly the voice playing this
                // note. Phase 1 just releases voice 0 if it matches.
                if self.voices[0].note == Some(note) {
                    self.voices[0].note_off();
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
        }

        let n_frames = samples.len() / 2;
        for frame in 0..n_frames {
            // Sum all voices. Most will be idle and return 0.0 cheaply.
            let mut mono: f32 = 0.0;
            for voice in &mut self.voices {
                mono += voice.tick();
            }
            // Voice output is unscaled (osc * env * vel). The post-synth
            // DSP chain handles patch gain / limiter — but a bare saw
            // peaks at ~±1, which is too hot for the limiter to clean
            // up. Scale by 0.5 here to land in the same ballpark as the
            // soundfont and plugin backends.
            let stereo = mono * 0.5;
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
}
