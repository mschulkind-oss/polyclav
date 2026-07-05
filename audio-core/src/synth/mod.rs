//! Native pure-Rust analog-style synth backend.
//!
//! Phase 1+ of `docs/ROADMAP.md`: a single hardcoded "minimoog" engine —
//! three PolyBLEP oscillators (saw/square/pulse, per-osc octave/detune/
//! level) plus a white-noise source into a renormalized mixer, into a
//! Moog ladder filter (with resonance + filter envelope) into an ADSR
//! amp env — rendered into the same interleaved stereo buffer the rest
//! of `audio-core` uses. No LFO, no mod matrix, no glide yet. At the
//! stage-3 defaults (osc 2/3 + noise levels 0) the render is
//! bit-identical to the Phase 1 single-saw engine.
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
use oscillator::{OscParams, Waveform};
use voice::{FilterEnvParams, Voice};

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
            filter_env: FilterEnvParams::default_minimoog(),
            osc_params: OscParams::default_bank(),
            noise_level: 0.0,
            glide_s: 0.0,
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
            voice.set_filter_env(self.filter_env);
            for (idx, params) in self.osc_params.iter().enumerate() {
                voice.set_osc(idx, *params);
            }
            voice.set_noise_level(self.noise_level);
            voice.set_glide(self.glide_s);
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

#[cfg(test)]
mod tests {
    use super::*;

    /// Press `note` at velocity 100.
    fn press(synth: &mut NativeSynth, note: u8) {
        synth.handle_event(&MidiEvent::NoteOn {
            channel: 0,
            note,
            velocity: 100,
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
}
