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

#ifdef __cplusplus
}
#endif

#endif /* POLYCLAV_AUDIO_H */