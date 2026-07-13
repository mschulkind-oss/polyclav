#ifndef POLYCLAV_AUDIO_H
#define POLYCLAV_AUDIO_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

int32_t polyclav_audio_start(void);
void polyclav_audio_stop(void);

/* Returns 1 if libsfizz is available (SFZ playback possible), else 0. sfizz
 * is dlopen'd lazily and is optional; safe to call before start. */
int32_t polyclav_audio_sfizz_available(void);

/* Soundfont path: NULL or empty -> sine fallback. Must be set BEFORE start. */
int32_t polyclav_audio_set_soundfont(const char *path);

/* Requested audio buffer size in frames — polyclav's own latency knob.
 * Clamped to [16, 8192]; 0 selects the default (128, ~2.7 ms at 48 kHz).
 * A *request*: the effective buffer never drops below what the platform
 * supports (the PipeWire graph quantum on Linux, the device's minimum
 * buffer on macOS). Must be set BEFORE start; read once when the audio
 * thread starts. */
void polyclav_audio_set_latency_frames(uint32_t frames);

/* Reload the soundfont set by polyclav_audio_set_soundfont(). Loads on a
 * background thread; the audio thread picks up the new backend on the
 * next callback. Returns 0 if reload was scheduled, 1 if no soundfont
 * is set or audio is not running. */
int32_t polyclav_audio_reload_soundfont(void);

/* Load and switch to an LV2 plugin identified by URI (e.g.
 * "http://geontime.com/dexed"). Discovery + instantiation happen on a
 * background worker thread; the audio thread picks up the new backend on
 * the next callback. Shares the soundfont generation counter so rapid
 * patch-switching still discards stale backends. Phase 1: plugins boot
 * to their default state (no preset/state loading).
 *
 * Returns 0 if load was scheduled, 1 if audio is not running, 2 if uri
 * is NULL or not valid UTF-8. */
int32_t polyclav_audio_set_lv2_plugin(const char *uri);

/* Load and switch to a CLAP plugin identified by .clap bundle path and
 * internal plugin id (e.g. "/nix/store/.../Dexed.clap" +
 * "com.asb2m10.dexed"). Same lifecycle as polyclav_audio_set_lv2_plugin.
 *
 * Returns 0 if load was scheduled, 1 if audio is not running, 2 if
 * either argument is NULL or not valid UTF-8. */
int32_t polyclav_audio_set_clap_plugin(const char *bundle_path, const char *plugin_id);

/* Load and switch to a native pure-Rust synth patch. `engine` selects
 * one of the factory-preset names baked into audio-core. Phase 1 only
 * supports "minimoog". Same lifecycle as the other plugin loaders.
 *
 * Returns 0 if load was scheduled, 1 if audio is not running, 2 if
 * `engine` is NULL or not valid UTF-8, 3 if the engine name is unknown. */
int32_t polyclav_audio_set_native_patch(const char *engine);

/* Offline (no-device) render: the native `engine` synth playing note/velocity
 * held from t=0, through the full DSP chain, written to `out` as interleaved
 * stereo f32 (48 kHz). `out` must hold at least n_frames*2 floats. Powers
 * `polyclav render` and the CI offline-render gate; works on every platform,
 * opens no audio device. Returns 0 on success, 2 on a bad/unknown engine, 3 if
 * out is NULL or n_frames is 0. */
int32_t polyclav_render_offline(const char *engine, uint8_t note, uint8_t velocity,
                                float *out, uint32_t n_frames);

/* One event for polyclav_render_offline_events, timed by absolute frame
 * offset from the start of the render (not a delta). kind: 0=NoteOn,
 * 1=NoteOff, 2=ControlChange, 3=PitchBend. NoteOn/NoteOff: data1=note,
 * data2=velocity (NoteOff ignores data2). ControlChange: data1=controller,
 * data2=value. PitchBend: data2=14-bit bend value, data1 unused. */
typedef struct {
    uint32_t frame;
    uint8_t kind;
    uint8_t channel;
    uint8_t data1;
    uint16_t data2;
} PolyclavMidiEvent;

/* Offline (no-device) render of an arbitrary timed MIDI event sequence
 * (e.g. a parsed Standard MIDI File) through ANY patch type, written to
 * `out` as interleaved stereo f32 (48 kHz). patch_type is one of
 * "soundfont" (patch_ref = file path, dispatches on extension),
 * "native" (patch_ref = engine name), "lv2" (patch_ref = URI, Linux
 * only), or "clap" (patch_ref = bundle path, plugin_id required, Linux
 * only). events must be sorted by frame ascending; pass NULL/0 for no
 * events. Returns 0 on success, 2 on an unknown/unavailable patch_type,
 * a bad string, or a load failure, 3 if out is NULL or n_frames is 0. */
int32_t polyclav_render_offline_events(const char *patch_type, const char *patch_ref,
                                       const char *plugin_id,
                                       const PolyclavMidiEvent *events, uint32_t n_events,
                                       float *out, uint32_t n_frames);

/* Measure the integrated (ungated) LUFS loudness of an interleaved stereo
 * f32 buffer at 48 kHz (ITU-R BS.1770-4 K-weighting; see dsp::loudness in
 * the Rust source for exactly what this does and does not measure). Meant
 * for offline analysis of a buffer from polyclav_render_offline, not the
 * real-time callback. Returns -inf for a NULL pointer, zero length, or
 * true silence. */
float polyclav_measure_lufs(const float *samples, uint32_t len);

/* Peak level (dBFS) of an interleaved stereo f32 buffer. Same NULL/empty/
 * silence handling as polyclav_measure_lufs. */
float polyclav_measure_peak_dbfs(const float *samples, uint32_t len);

/* MIDI event push (Go -> Rust audio thread, lock-free, drops on queue full). */
void polyclav_midi_note_on(uint8_t channel, uint8_t note, uint8_t velocity);
void polyclav_midi_note_off(uint8_t channel, uint8_t note, uint8_t velocity);
void polyclav_midi_cc(uint8_t channel, uint8_t controller, uint8_t value);
void polyclav_midi_pitch_bend(uint8_t channel, uint16_t bend);

/* DSP parameter setters. All values clamped to [0.0, 1.0] in Rust. The
 * audio thread reads atomically on each callback; updates are advisory
 * and take effect on the next process. */
void polyclav_dsp_set_master_volume(float v);
void polyclav_dsp_set_compressor(float v);
void polyclav_dsp_set_reverb(float v);

/* Per-patch gain as a linear multiplier. Go converts dB -> linear before
 * pushing. Default 1.0, clamped to [0.0, 8.0] in Rust. */
void polyclav_dsp_set_patch_gain(float linear);

/* Mastering compressor amount in [0.0, 1.0]. 0 = bypass. */
void polyclav_dsp_set_mastering_compressor(float amount);

/* Brick-wall limiter ceiling in dBFS. Default -0.3, clamped to [-12.0, 0.0]. */
void polyclav_dsp_set_limiter_ceiling_db(float db);

/* Drive-pedal amount in [0.0, 1.0]. Default 0.0 = bit-exact bypass. Runs
 * in the shared post-synth DSP chain (before patch gain), so it applies
 * to every synth backend, not just the native synth. */
void polyclav_dsp_set_drive_pedal(float v);

/* Analog-delay time in milliseconds, clamped to [1, 1000]. */
void polyclav_dsp_set_analog_delay_time_ms(float ms);

/* Analog-delay feedback (repeats) amount, clamped to [0.0, 0.9] — capped
 * below unity so the pedal stays a delay, not a deliberate self-oscillator. */
void polyclav_dsp_set_analog_delay_feedback(float v);

/* Analog-delay wet/dry mix in [0.0, 1.0]. Default 0.0 = bit-exact bypass.
 * Runs in the shared post-synth DSP chain, after the drive pedal, so it
 * applies to every synth backend. */
void polyclav_dsp_set_analog_delay_mix(float v);

/* Native synth filter cutoff in Hz, pushed from the FILTER page's
 * Cutoff knob (MAIN knob 4 now drives the drive pedal instead). The
 * audio thread reads the atomic per block and applies it to the active
 * native synth (no-op for other backends). Clamped to [20, 20000] in
 * Rust. */
void polyclav_dsp_set_native_cutoff_hz(float hz);

/* Native synth filter resonance (Q). Same lifecycle as the cutoff
 * setter: the audio thread reads the atomic per block and applies it
 * to the active native synth (no-op for other backends). Default 0.3;
 * clamped to [0.0, 0.95] in Rust — headroom below the Stilson/Smith
 * ladder's self-oscillation instability. */
void polyclav_dsp_set_native_resonance(float v);

/* Native synth filter-envelope (env 2): ADSR + env->cutoff amount.
 * Same lifecycle as the cutoff setter: the audio thread reads the
 * atomics per block and applies them to the active native synth (no-op
 * for other backends). effective_cutoff = base_cutoff * 2^(amount *
 * env * 4.0), i.e. amount in [0,1] sweeps up to +4 octaves above the
 * knob cutoff, clamped to [20, 20000] Hz. Times clamped to [0.0001,
 * 10] s, sustain and amount to [0, 1] in Rust. Defaults 5 ms / 600 ms
 * / 0.4 / 600 ms with amount 0.0 (modulation OFF). */
void polyclav_dsp_set_native_filter_env(float attack_s, float decay_s, float sustain,
                                        float release_s, float amount);

/* Native synth oscillator bank (stage 3). idx is 0..2; wave is 0=saw,
 * 1=square, 2=pulse (pulse runs a fixed 25% duty for this stage);
 * octave clamps to [-2, 2]; detune_cents to [-100, 100]; level to
 * [0, 1]. Out-of-range idx or wave is ignored (with an eprintln on the
 * Rust side). Same lifecycle as the cutoff setter: the audio thread
 * reads the atomics per block and applies them to the active native
 * synth (no-op for other backends). Defaults keep osc 2/3 silent
 * (level 0) with Moog-ish offsets pre-dialed: osc 1 saw/0 oct/0
 * cents/1.0, osc 2 saw/0 oct/-7 cents/0.0, osc 3 saw/-1 oct/+5
 * cents/0.0 — so the default render is unchanged and turning a level
 * up immediately sounds right. */
void polyclav_dsp_set_native_osc(int32_t idx, int32_t wave, int32_t octave,
                                 float detune_cents, float level);

/* Native synth white-noise mixer level in [0, 1] (clamped in Rust).
 * Default 0.0 = silent. Same lifecycle as the cutoff setter. */
void polyclav_dsp_set_native_noise(float level);

/* Native synth glide (portamento) time constant in seconds, clamped to
 * [0, 5] in Rust. Default 0.0 = no slew (pitch jumps instantly; render
 * identical to the pre-glide engine). When enabled, the voice's base
 * frequency slews exponentially toward the note pitch; glide applies
 * to legato hand-offs AND retriggered notes of a still-sounding voice
 * (Minimoog behavior), while a voice starting from silence begins at
 * its target pitch. Same lifecycle as the cutoff setter. */
void polyclav_dsp_set_native_glide(float seconds);

/* Native synth amp-envelope (env 1) ADSR. Times clamped to [0.0001,
 * 10] s, sustain to [0, 1] in Rust. Defaults 5 ms / 200 ms / 0.7 /
 * 400 ms — exactly the previously-hardcoded values, so the default
 * render is unchanged. Updating params does not disturb a running
 * envelope. Same lifecycle as the cutoff setter. */
void polyclav_dsp_set_native_amp_env(float attack_s, float decay_s, float sustain,
                                     float release_s);

/* Native synth pulse-wave duty cycle, clamped to [0.05, 0.95] in Rust.
 * One global knob shared by all three oscillators; only audible while a
 * pulse waveform is selected. Default 0.25 (the old fixed duty — render
 * unchanged at the default). Same lifecycle as the cutoff setter. */
void polyclav_dsp_set_native_pulse_width(float width);

/* Native synth pre-filter tanh drive amount, clamped to [0, 1] in
 * Rust. Default 0.0 = bit-exact bypass. When > 0 the post-mixer signal
 * is shaped by tanh(x * g) / tanh(g) with g = 1 + drive*4 before the
 * ladder filter — peak-referenced normalization: unity at |x| = 1,
 * small-signal gain g/tanh(g) >= 1 (drive adds loudness + compression
 * instead of dropping the level). Same lifecycle as the cutoff
 * setter. */
void polyclav_dsp_set_native_drive(float drive);

/* Native synth velocity routing, both amounts clamped to [0, 1] in
 * Rust. to_amp scales the per-note amplitude:
 * scale = lerp(1.0, vel/127, to_amp) — default 1.0 is exactly the
 * classic vel/127 (render unchanged); 0 ignores velocity. to_cutoff
 * modulates the effective filter cutoff by
 * 2^(to_cutoff * (vel/127 - 0.5) * 2) — up to +/-1 octave around the
 * knob cutoff, centered at velocity 64; default 0.0 = bypass. Both are
 * captured per voice at note-on (knob turns mid-note affect the next
 * note). Composes multiplicatively with the filter-env and
 * keyboard-tracking cutoff modulation; the final effective cutoff is
 * clamped to [20, 20000] Hz. Same lifecycle as the cutoff setter. */
void polyclav_dsp_set_native_vel_routing(float to_cutoff, float to_amp);

/* Native synth keyboard tracking of the filter cutoff, clamped to
 * [0, 1] in Rust. The effective cutoff is multiplied by
 * 2^(amt * (note - 60) / 12) — at 1.0 it tracks the keyboard 100%
 * (2x per octave above middle C, /2 per octave below), following the
 * sounding note (legato hand-offs included). Default 0.0 = bypass.
 * Composes multiplicatively with the filter-env and velocity cutoff
 * modulation; the final effective cutoff is clamped to [20, 20000] Hz.
 * Same lifecycle as the cutoff setter. */
void polyclav_dsp_set_native_kbd_track(float amt);

/* Native synth GLOBAL LFO (one LFO shared across voices, advanced once
 * per sample). wave is 0=triangle, 1=saw, 2=square, 3=sample-and-hold
 * (deterministic xorshift stepped once per LFO cycle); out-of-range
 * codes are ignored (with an eprintln on the Rust side). rate_hz is
 * clamped to [0.05, 20] (default 5.0). Depths all default 0 =
 * bit-transparent bypass:
 *   to_pitch_cents [0, 100] — vibrato: voice freq * 2^(lfo*cents/1200).
 *     The depth heard is scaled LIVE by MIDI CC 1 (mod wheel); the
 *     synth boots with the wheel at 1.0 so a configured depth sounds
 *     without a wheel, and the first CC 1 event takes over (wheel 0
 *     silences vibrato — classic vibrato-on-wheel).
 *   to_cutoff_oct [0, 2]   — effective cutoff * 2^(lfo*oct), composed
 *     multiplicatively with the env/vel/kbd cutoff modulation (final
 *     cutoff clamped to [20, 20000] Hz).
 *   to_amp [0, 1]          — tremolo: output * (1 - depth*(lfo*0.5+0.5)).
 * Same lifecycle as the cutoff setter (no-op for other backends). */
void polyclav_dsp_set_native_lfo(uint32_t wave, float rate_hz, float to_pitch_cents,
                                 float to_cutoff_oct, float to_amp);

/* Native synth pitch-bend range in semitones at full deflection,
 * clamped to [0, 12] in Rust; default 2.0 (the MIDI convention).
 * polyclav_midi_pitch_bend events (14-bit value, 8192 = centre) scale
 * the voice frequency by 2^(range * (bend-8192)/8192 / 12); with no
 * bend event the factor is exactly 1.0 and the render is unchanged.
 * Same lifecycle as the cutoff setter (no-op for other backends). */
void polyclav_dsp_set_native_bend_range(float st);

/* Native synth voice-allocation mode: 0 = mono_legato (default —
 * 1 voice, last-note priority, envelopes only retrigger when no other
 * key is held; bit-identical to the pre-poly engine), 1 = mono_retrig
 * (1 voice, envelopes ALWAYS retrigger on note-on), 2 = poly (8
 * voices; a note-on takes a free voice, else steals the oldest voice
 * already in its release tail, else the oldest held voice; a note-off
 * releases exactly the voice(s) sounding that note). Out-of-range codes are ignored (with an eprintln on the Rust
 * side). Switching modes while notes sound releases every voice (no
 * stuck notes — held keys fade out and must be re-pressed). Same
 * lifecycle as the cutoff setter (no-op for other backends). */
void polyclav_dsp_set_native_voice_mode(uint32_t mode);

/* Native synth 2x oversampling of the per-voice nonlinear section
 * (tanh drive + Moog ladder): 0 = off (default — base-rate path,
 * bit-identical to the pre-oversampling engine), 1 = on (mixer output
 * is upsampled 2x through a minimum-phase halfband, drive + ladder run
 * retuned at sample_rate * 2, then decimated back — removes the tanh
 * stages' fold-back aliasing under hard drive). Out-of-range codes are
 * ignored (with an eprintln on the Rust side). Toggling while notes
 * sound swaps per-voice filter instances (reset + retuned) — a brief
 * click may be audible; treat as a setup switch, not a performance
 * control. Same lifecycle as the cutoff setter (no-op for other
 * backends). */
void polyclav_dsp_set_native_oversample(uint32_t on);

#ifdef __cplusplus
}
#endif

#endif /* POLYCLAV_AUDIO_H */