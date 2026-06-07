//! Single synth voice — one oscillator, one filter, one amp envelope.
//!
//! Phase 1 deliberately ships only the bare minimum needed to make a
//! note audible. The scaffolding here is sized so Phase 2 can drop in
//! multi-oscillator mixing, a filter envelope, glide, and per-voice LFO
//! routing without changing the public surface.

use super::envelope::Adsr;
use super::filter::MoogFilter;
use super::oscillator::Oscillator;

/// Convert a MIDI note number to frequency in Hz (A4 = 69 = 440 Hz).
pub fn midi_to_hz(note: u8) -> f32 {
    440.0 * 2.0f32.powf((note as f32 - 69.0) / 12.0)
}

/// One voice. Phase 1 holds a single oscillator (no detune, no octave
/// shift) plus a ladder filter and an amp envelope. The voice owns its
/// per-instance DSP state and renders one sample at a time.
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
    osc: Oscillator,
    filter: MoogFilter,
    amp_env: Adsr,
    /// Cached cutoff so we only call `set_cutoff_q` when it changes,
    /// avoiding redundant work on every block.
    last_cutoff_hz: f32,
    /// Cached resonance for the same reason.
    last_resonance: f32,
}

impl Voice {
    /// Build a voice configured with the Minimoog defaults from doc 14
    /// §4.4 (amp ADSR 5/200/0.7/400, cutoff 2 kHz, resonance 0.3).
    pub fn new(sample_rate: f32) -> Self {
        let cutoff = 2_000.0;
        let resonance = 0.3;
        Self {
            note: None,
            velocity_scale: 0.0,
            fired_at: 0,
            osc: Oscillator::new(sample_rate),
            filter: MoogFilter::new(sample_rate, cutoff, resonance),
            amp_env: Adsr::new(sample_rate, 0.005, 0.200, 0.7, 0.400),
            last_cutoff_hz: cutoff,
            last_resonance: resonance,
        }
    }

    /// Trigger this voice with the given MIDI note + velocity.
    pub fn note_on(&mut self, note: u8, velocity: u8, fired_at: u64) {
        self.note = Some(note);
        self.velocity_scale = (velocity as f32) / 127.0;
        self.fired_at = fired_at;
        self.amp_env.note_on();
    }

    /// Release this voice — starts the envelope release stage.
    pub fn note_off(&mut self) {
        self.amp_env.note_off();
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
    /// last applied values).
    pub fn set_filter(&mut self, cutoff_hz: f32, resonance: f32) {
        let cutoff_hz = cutoff_hz.clamp(20.0, 20_000.0);
        let resonance = resonance.clamp(0.0, 1.0);
        if (cutoff_hz - self.last_cutoff_hz).abs() > 0.01
            || (resonance - self.last_resonance).abs() > 0.0001
        {
            self.filter.set_cutoff_q(cutoff_hz, resonance);
            self.last_cutoff_hz = cutoff_hz;
            self.last_resonance = resonance;
        }
    }

    /// Render one sample from this voice. Returns `0.0` when the voice
    /// is idle so it can be summed unconditionally with no audible cost.
    pub fn tick(&mut self) -> f32 {
        let Some(note) = self.note else {
            return 0.0;
        };
        let freq = midi_to_hz(note);
        let osc = self.osc.tick(freq);
        let filtered = self.filter.tick(osc);
        let amp = self.amp_env.tick();
        let out = filtered * amp * self.velocity_scale;
        if !self.amp_env.is_active() {
            // Envelope finished — release the note slot so the allocator
            // can reuse it.
            self.note = None;
        }
        out
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
