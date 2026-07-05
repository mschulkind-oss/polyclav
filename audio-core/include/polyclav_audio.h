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

/* Native synth filter cutoff in Hz. Phase 1 hardcoded knob-4 mapping:
 * Go pushes this whenever knob 4 turns. The audio thread reads the
 * atomic per block and applies it to the active native synth (no-op
 * for other backends). Clamped to [20, 20000] in Rust. */
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

#ifdef __cplusplus
}
#endif

#endif /* POLYCLAV_AUDIO_H */